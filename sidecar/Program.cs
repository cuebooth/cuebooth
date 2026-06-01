// CueBooth PowerPoint sidecar — entry point.
//
// Hosts two long-running services:
//   * SlideMonitor — subscribes to PowerPoint COM events and forwards
//     {slide_index, total_slides, title, notes_text} payloads on slide change.
//   * SidecarPipeServer — accepts a single Go-server connection over a
//     named pipe (\\.\pipe\cuebooth-sidecar) and delivers payloads to it
//     as newline-delimited JSON.
//
// See ../docs/design.md §3.3 (PowerPoint Monitor) and §5 (Phase 4 —
// Slide Engine).

using CueBooth.Sidecar;

var builder = Host.CreateApplicationBuilder(args);
builder.Services.AddSingleton<SidecarPipeServer>();
builder.Services.AddHostedService(sp => sp.GetRequiredService<SidecarPipeServer>());
builder.Services.AddHostedService<SlideMonitor>();

var host = builder.Build();
host.Run();
