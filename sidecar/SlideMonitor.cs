using System.Runtime.InteropServices;
using System.Text.Json;
using Microsoft.Office.Interop.PowerPoint;

namespace CueBooth.Sidecar;

/// <summary>
/// Detects PowerPoint slide changes during an active slideshow and emits a
/// slide-changed payload on each transition.
///
/// CB-006 implements this by polling the active slideshow's
/// <c>CurrentShowPosition</c> on a short timer — a simple, dependency-free
/// approach that runs on an ordinary worker thread: outbound COM calls marshal
/// into PowerPoint's own process, so no STA thread or message pump is needed.
/// CB-040 supersedes polling with COM event sinks (which *do* require an STA
/// thread + pump) to cut latency and idle CPU; the emitted payload and the
/// pipe contract are unchanged by that swap. See docs/design.md §3.3 and §6.
/// </summary>
internal sealed class SlideMonitor : BackgroundService
{
    // Emit snake_case JSON keys to match the project's wire-format convention
    // (docs/protocol.md — server_version, level_db, ...). NOTE: this is the
    // internal sidecar→server pipe format, not the WebSocket `slides` state
    // shape — the field *names* differ (this uses slide_index/total_slides and
    // carries notes_text; the WS block uses current/total). Only the casing
    // convention is shared.
    private static readonly JsonSerializerOptions _jsonOptions =
        new() { PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower };

    // Slide changes are operator-paced (seconds apart), so a sub-second poll is
    // imperceptible. CB-040's event-based detection removes this latency floor
    // and the idle polling cost entirely.
    private static readonly TimeSpan _pollInterval = TimeSpan.FromMilliseconds(250);
    private static readonly TimeSpan _attachRetry = TimeSpan.FromSeconds(5);

    private readonly ILogger<SlideMonitor> _log;
    private readonly SidecarPipeServer _pipe;

    public SlideMonitor(ILogger<SlideMonitor> log, SidecarPipeServer pipe)
    {
        _log = log;
        _pipe = pipe;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        // Awaited (not fire-and-forget): failures surface to the host and the
        // service's lifetime reflects the monitor's, instead of reporting
        // "completed" immediately.
        while (!stoppingToken.IsCancellationRequested)
        {
            var app = TryAttach();
            if (app is null)
            {
                await DelayQuietly(_attachRetry, stoppingToken).ConfigureAwait(false);
                continue;
            }
            var faulted = false;
            try
            {
                await PollLoopAsync(app, stoppingToken).ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
                // Shutting down.
            }
            catch (COMException ex)
            {
                // PowerPoint closed mid-show; drop the reference and re-attach.
                _log.LogInformation(ex, "PowerPoint connection lost; will re-attach");
                faulted = true;
            }
            catch (Exception ex)
            {
                // Never let an unexpected error kill the monitor permanently;
                // log and fall through to re-attach.
                _log.LogWarning(ex, "slide monitor error; will re-attach");
                faulted = true;
            }
            finally
            {
                Release(app);
            }
            // Back off after a fault (the app RCW is already released above) so a
            // deterministic recurring poll error doesn't churn the COM attach and
            // spam logs every poll interval. The null-attach path backs off above.
            if (faulted) await DelayQuietly(_attachRetry, stoppingToken).ConfigureAwait(false);
        }
    }

    /// <summary>
    /// Attach to an <em>already-running</em> PowerPoint via the Running Object
    /// Table. Returns null if none is running (the caller retries on a slow
    /// timer until the operator's deck appears). We deliberately avoid
    /// <c>new Application()</c>, which launches a fresh hidden PowerPoint
    /// instead of attaching to the open deck. <c>Marshal.GetActiveObject</c>
    /// isn't available on .NET (Core+), so it's P/Invoked below.
    /// </summary>
    private Application? TryAttach()
    {
        try
        {
            CLSIDFromProgID("PowerPoint.Application", out var clsid);
            GetActiveObject(ref clsid, IntPtr.Zero, out var obj);
            _log.LogInformation("attached to running PowerPoint");
            return (Application)obj;
        }
        catch (Exception ex)
        {
            // Catch broadly, not just COMException: besides the expected
            // MK_E_UNAVAILABLE (PowerPoint not running yet), the cast can throw
            // InvalidCastException if the ROT hands back an unexpected object, and
            // the P/Invokes can surface non-COM marshaling failures. This runs
            // outside ExecuteAsync's try, so an escaping exception would bypass
            // the catch-all and — with the default StopHost behavior — take down
            // the whole sidecar. Return null and let the caller retry.
            _log.LogDebug(ex, "could not attach to PowerPoint; will retry");
            return null;
        }
    }

    private async Task PollLoopAsync(Application app, CancellationToken ct)
    {
        using var timer = new PeriodicTimer(_pollInterval);
        var lastPosition = -1;

        // Per-tick top-level RCWs (the SlideShowWindows collection and the show
        // window) are released below in finally blocks, since at 250 ms they'd
        // otherwise accumulate fast. TODO(CB-040): the deeper extraction chains
        // (show.View, .Slide, .Presentation.Slides, the title/notes hops) still
        // create RCWs that aren't individually released — deferred to the CB-040
        // rework where leak behavior can be verified on Windows.
        while (await timer.WaitForNextTickAsync(ct).ConfigureAwait(false))
        {
            // SlideShowWindows is empty unless a show is running; only report
            // during an active slideshow (the operator running their deck).
            var windows = app.SlideShowWindows;
            try
            {
                if (windows.Count < 1)
                {
                    lastPosition = -1; // re-emit the opening slide when a show (re)starts
                    continue;
                }

                var show = windows[1];
                try
                {
                    var position = show.View.CurrentShowPosition;
                    if (position == lastPosition) continue; // de-dup: emit only on change

                    // Advance the de-dup state only after a successful emit: if Emit
                    // fails (it swallows/logs COM hiccups), lastPosition stays put so
                    // the next tick retries this slide instead of skipping it until
                    // the operator navigates away and back.
                    if (Emit(show)) lastPosition = position;
                }
                finally { Release(show); }
            }
            finally { Release(windows); }
        }
    }

    // Returns true if the payload was built and queued; false on a logged
    // failure, so the caller can retry on the next poll tick.
    private bool Emit(SlideShowWindow show)
    {
        try
        {
            var slide = show.View.Slide;
            // slide_index and total_slides are both file-relative: Slide.SlideIndex
            // is the slide's 1-based position in the presentation, paired with the
            // file's Slides.Count. (CurrentShowPosition — used above for de-dup —
            // is show-relative and would disagree with Slides.Count when the deck
            // has hidden slides or runs a custom show, e.g. "20 of 24" at the end.)
            var payload = new SlideChangedPayload(
                SlideIndex: slide.SlideIndex,
                TotalSlides: show.Presentation.Slides.Count,
                Title: ReadTitle(slide),
                NotesText: ReadNotes(slide));

            var json = JsonSerializer.Serialize(payload, _jsonOptions);
            _pipe.Broadcast(json);
            _log.LogInformation("slide change {Index}/{Total}", payload.SlideIndex, payload.TotalSlides);
            return true;
        }
        catch (Exception ex)
        {
            _log.LogWarning(ex, "failed to emit slide change");
            return false;
        }
    }

    private static string ReadTitle(Slide slide)
    {
        try
        {
            // The human-readable title lives in the title placeholder, not in
            // slide.Name (the internal object name, e.g. "Slide1"). protocol.md's
            // `slides.title` is the actual title text. Shapes.Title throws when
            // the slide has no title placeholder, so this is guarded.
            return slide.Shapes.Title.TextFrame.TextRange.Text ?? string.Empty;
        }
        catch
        {
            // No title placeholder, or COM threw — emit empty rather than a
            // misleading internal name.
            return string.Empty;
        }
    }

    private static string ReadNotes(Slide slide)
    {
        try
        {
            // Notes live on a separate NotesPage shape collection.
            var placeholder = slide.NotesPage.Shapes.Placeholders[2];
            return placeholder.TextFrame.TextRange.Text ?? string.Empty;
        }
        catch
        {
            return string.Empty;
        }
    }

    private static async Task DelayQuietly(TimeSpan delay, CancellationToken ct)
    {
        try { await Task.Delay(delay, ct).ConfigureAwait(false); }
        catch (OperationCanceledException) { /* shutting down */ }
    }

    private static void Release(object? comObject)
    {
        if (comObject is not null && Marshal.IsComObject(comObject))
        {
            try { Marshal.FinalReleaseComObject(comObject); }
            catch { /* already released / detached */ }
        }
    }

    // Marshal.GetActiveObject was not ported to .NET (Core+); P/Invoke the
    // underlying OLE automation entry points to attach to a running instance.
    [DllImport("ole32.dll", PreserveSig = false)]
    private static extern void CLSIDFromProgID(
        [MarshalAs(UnmanagedType.LPWStr)] string progId, out Guid clsid);

    [DllImport("oleaut32.dll", PreserveSig = false)]
    private static extern void GetActiveObject(
        ref Guid clsid, IntPtr reserved,
        [MarshalAs(UnmanagedType.IUnknown)] out object obj);
}

/// <summary>
/// Wire format for slide-changed events sent to the Go server over the named
/// pipe: snake_case keys {slide_index, total_slides, title, notes_text}. Only
/// the snake_case *casing* follows docs/protocol.md — that document is the
/// normative spec for the client↔server WebSocket protocol, not this
/// sidecar↔server pipe contract, which is formalized in CB-041 (#31). Distinct
/// from the WebSocket `slides` state shape (current/total).
/// </summary>
internal sealed record SlideChangedPayload(
    int SlideIndex,
    int TotalSlides,
    string Title,
    string NotesText);
