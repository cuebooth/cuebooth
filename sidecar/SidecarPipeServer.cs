using System.IO.Pipes;
using System.Text;
using System.Threading.Channels;

namespace CueBooth.Sidecar;

/// <summary>
/// One-way named-pipe server that delivers slide-changed payloads to the Go
/// server as newline-delimited JSON. A single connection at a time — the Go
/// server is the only consumer.
///
/// Payloads are handed off through a bounded channel: producers (the slide
/// monitor) enqueue without blocking, and a single drain loop is the only
/// writer to the pipe. This keeps slide detection off pipe I/O — a slow or
/// stalled consumer can't block the monitor — and guarantees each JSON line is
/// written atomically, with no byte interleaving from concurrent writes.
///
/// IPC choice: named pipe over localhost WebSocket because (a) no port to
/// configure, (b) Windows-native security via pipe ACLs if needed later,
/// (c) the Go side can use \\.\pipe\cuebooth-sidecar with the os package.
///
/// TODO(security): the pipe uses the default security descriptor, so any local
/// user could connect and read slide notes. On a shared machine, restrict it
/// to the current user (NamedPipeServerStreamAcl.Create with a PipeSecurity).
/// Deferred — low risk on a single-operator production PC.
/// </summary>
internal sealed class SidecarPipeServer : BackgroundService
{
    public const string PipeName = "cuebooth-sidecar";

    private readonly ILogger<SidecarPipeServer> _log;

    // Bounded + DropOldest: if no consumer is connected the queue can't grow
    // without bound, and for slide state it's the most recent entries that
    // matter. Single reader — the drain loop in ExecuteAsync.
    private readonly Channel<string> _queue = Channel.CreateBounded<string>(
        new BoundedChannelOptions(256)
        {
            FullMode = BoundedChannelFullMode.DropOldest,
            SingleReader = true,
        });

    public SidecarPipeServer(ILogger<SidecarPipeServer> log) => _log = log;

    /// <summary>
    /// Queue a JSON line for delivery. Non-blocking: returns immediately and
    /// never stalls the caller (the slide monitor's poll loop) on pipe I/O.
    /// Drops silently if the queue is full or the server is shutting down — the
    /// Go server is responsible for connecting before slides matter.
    /// </summary>
    public void Broadcast(string json) => _queue.Writer.TryWrite(json);

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        while (!stoppingToken.IsCancellationRequested)
        {
            var pipe = new NamedPipeServerStream(
                PipeName,
                PipeDirection.Out,
                maxNumberOfServerInstances: 1,
                PipeTransmissionMode.Byte,
                PipeOptions.Asynchronous);

            try
            {
                _log.LogInformation("waiting for Go server on \\\\.\\pipe\\{Pipe}", PipeName);
                await pipe.WaitForConnectionAsync(stoppingToken).ConfigureAwait(false);
                _log.LogInformation("Go server connected");
                await PumpAsync(pipe, stoppingToken).ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
                break; // shutting down
            }
            catch (Exception ex)
            {
                _log.LogWarning(ex, "pipe error; recycling");
            }
            finally
            {
                try { await pipe.DisposeAsync().ConfigureAwait(false); } catch { /* ignore */ }
            }
        }
    }

    // Single writer to the pipe: drain queued lines until the client
    // disconnects or shutdown is requested, then return so the accept loop can
    // recycle the pipe for the next connection.
    private async Task PumpAsync(NamedPipeServerStream pipe, CancellationToken ct)
    {
        var reader = _queue.Reader;
        while (await reader.WaitToReadAsync(ct).ConfigureAwait(false))
        {
            while (reader.TryRead(out var json))
            {
                if (!pipe.IsConnected) return;
                var bytes = Encoding.UTF8.GetBytes(json + "\n");
                await pipe.WriteAsync(bytes, ct).ConfigureAwait(false);
                await pipe.FlushAsync(ct).ConfigureAwait(false);
            }
        }
    }
}
