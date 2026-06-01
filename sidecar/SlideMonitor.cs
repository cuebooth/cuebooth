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
                await DelayQuietly(_attachRetry, stoppingToken);
                continue;
            }
            try
            {
                await PollLoopAsync(app, stoppingToken);
            }
            catch (OperationCanceledException)
            {
                // Shutting down.
            }
            catch (COMException ex)
            {
                // PowerPoint closed mid-show; drop the reference and re-attach.
                _log.LogInformation(ex, "PowerPoint connection lost; will re-attach");
            }
            catch (Exception ex)
            {
                // Never let an unexpected error kill the monitor permanently;
                // log and fall through to re-attach.
                _log.LogWarning(ex, "slide monitor error; re-attaching");
            }
            finally
            {
                Release(app);
            }
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
        catch (COMException)
        {
            // MK_E_UNAVAILABLE etc. — PowerPoint isn't running yet.
            return null;
        }
    }

    private async Task PollLoopAsync(Application app, CancellationToken ct)
    {
        using var timer = new PeriodicTimer(_pollInterval);
        var lastPosition = -1;

        // TODO(CB-040): each tick (and the title/notes extraction chains) creates
        // COM RCWs that should be released with Marshal.ReleaseComObject to avoid
        // leaking references and keeping the PowerPoint process alive. Polling
        // makes this more pressing than the event model would. Deferred to the
        // CB-040 rework, where leak behavior can be verified on Windows.
        while (await timer.WaitForNextTickAsync(ct))
        {
            // SlideShowWindows is empty unless a show is running; only report
            // during an active slideshow (the operator running their deck).
            if (app.SlideShowWindows.Count < 1)
            {
                lastPosition = -1; // re-emit the opening slide when a show (re)starts
                continue;
            }

            var show = app.SlideShowWindows[1];
            var position = show.View.CurrentShowPosition;
            if (position == lastPosition) continue; // de-dup: emit only on change

            lastPosition = position;
            Emit(show, position);
        }
    }

    private void Emit(SlideShowWindow show, int position)
    {
        try
        {
            var slide = show.View.Slide;
            var payload = new SlideChangedPayload(
                SlideIndex: position,
                TotalSlides: show.Presentation.Slides.Count,
                Title: ReadTitle(slide),
                NotesText: ReadNotes(slide));

            var json = JsonSerializer.Serialize(payload, _jsonOptions);
            _pipe.Broadcast(json);
            _log.LogInformation("slide change {Index}/{Total}", payload.SlideIndex, payload.TotalSlides);
        }
        catch (Exception ex)
        {
            _log.LogWarning(ex, "failed to emit slide change");
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
        try { await Task.Delay(delay, ct); }
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
/// Wire format for slide-changed events sent to the Go server. Serialized with
/// snake_case keys ({slide_index, total_slides, title, notes_text}) per the
/// project JSON convention (docs/protocol.md). Internal sidecar→server format,
/// distinct from the WebSocket `slides` state shape.
/// </summary>
internal sealed record SlideChangedPayload(
    int SlideIndex,
    int TotalSlides,
    string Title,
    string NotesText);
