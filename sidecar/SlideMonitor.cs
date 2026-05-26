using System.Text.Json;
using Microsoft.Office.Interop.PowerPoint;

namespace CueBooth.Sidecar;

/// <summary>
/// Subscribes to PowerPoint COM events and emits a slide-changed payload on
/// every slide transition. No polling — handlers are wired to the
/// Application.SlideShowNextSlide / SlideShowOnNext events.
/// </summary>
internal sealed class SlideMonitor : BackgroundService
{
    private readonly ILogger<SlideMonitor> _log;
    private readonly SidecarPipeServer _pipe;
    private Application? _ppt;

    public SlideMonitor(ILogger<SlideMonitor> log, SidecarPipeServer pipe)
    {
        _log = log;
        _pipe = pipe;
    }

    protected override Task ExecuteAsync(CancellationToken stoppingToken)
    {
        // Attach to the running PowerPoint instance (the user's slide deck
        // is already open). If it isn't running yet, retry on a slow timer
        // until it appears — this is intentionally lazy.
        _ = Task.Run(() => AttachLoopAsync(stoppingToken), stoppingToken);
        return Task.CompletedTask;
    }

    private async Task AttachLoopAsync(CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            try
            {
                _ppt ??= new Application();
                _ppt.SlideShowNextSlide += OnSlideShowNextSlide;
                _ppt.SlideShowOnNext += OnSlideShowOnNext;
                _ppt.SlideShowOnPrevious += OnSlideShowOnPrevious;
                _log.LogInformation("attached to PowerPoint COM");
                return;
            }
            catch (Exception ex)
            {
                _log.LogDebug(ex, "PowerPoint not available yet; will retry");
                try { await Task.Delay(TimeSpan.FromSeconds(5), ct); }
                catch (TaskCanceledException) { return; }
            }
        }
    }

    private void OnSlideShowNextSlide(SlideShowWindow window) => Emit(window);
    private void OnSlideShowOnNext(SlideShowWindow window) => Emit(window);
    private void OnSlideShowOnPrevious(SlideShowWindow window) => Emit(window);

    private void Emit(SlideShowWindow window)
    {
        try
        {
            var view = window.View;
            var slide = view.Slide;
            var notes = ExtractNotes(slide);
            var payload = new SlideChangedPayload(
                SlideIndex: view.CurrentShowPosition,
                TotalSlides: window.Presentation.Slides.Count,
                Title: SafeTitle(slide),
                NotesText: notes);

            var json = JsonSerializer.Serialize(payload);
            _pipe.Broadcast(json);
            _log.LogInformation("slide change {Index}/{Total}", payload.SlideIndex, payload.TotalSlides);
        }
        catch (Exception ex)
        {
            _log.LogWarning(ex, "failed to emit slide change");
        }
    }

    private static string ExtractNotes(Slide slide)
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

    private static string SafeTitle(Slide slide)
    {
        try { return slide.Name ?? string.Empty; }
        catch { return string.Empty; }
    }

    public override void Dispose()
    {
        if (_ppt is not null)
        {
            try
            {
                _ppt.SlideShowNextSlide -= OnSlideShowNextSlide;
                _ppt.SlideShowOnNext -= OnSlideShowOnNext;
                _ppt.SlideShowOnPrevious -= OnSlideShowOnPrevious;
            }
            catch { /* PowerPoint may already be gone */ }
        }
        base.Dispose();
    }
}

/// <summary>
/// Wire format for slide-changed events sent to the Go server.
/// </summary>
internal sealed record SlideChangedPayload(
    int SlideIndex,
    int TotalSlides,
    string Title,
    string NotesText);
