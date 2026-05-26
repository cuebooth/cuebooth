using System.IO.Pipes;
using System.Text;

namespace CueBooth.Sidecar;

/// <summary>
/// One-way named-pipe server that delivers slide-changed payloads to the Go
/// server as newline-delimited JSON. A single connection at a time — the Go
/// server is the only consumer.
///
/// IPC choice: named pipe over localhost WebSocket because (a) no port to
/// configure, (b) Windows-native security via pipe ACLs if needed later,
/// (c) the Go side can use \\.\pipe\cuebooth-sidecar with the os package.
/// </summary>
internal sealed class SidecarPipeServer : BackgroundService
{
    public const string PipeName = "cuebooth-sidecar";

    private readonly ILogger<SidecarPipeServer> _log;
    private readonly object _gate = new();
    private NamedPipeServerStream? _current;

    public SidecarPipeServer(ILogger<SidecarPipeServer> log) => _log = log;

    /// <summary>
    /// Send a single line (the JSON payload) to the connected client, if any.
    /// Drops the message silently if nothing's connected — the Go server is
    /// responsible for connecting before slides matter.
    /// </summary>
    public void Broadcast(string json)
    {
        NamedPipeServerStream? pipe;
        lock (_gate) { pipe = _current; }
        if (pipe is null || !pipe.IsConnected) return;

        try
        {
            var bytes = Encoding.UTF8.GetBytes(json + "\n");
            pipe.Write(bytes, 0, bytes.Length);
            pipe.Flush();
        }
        catch (Exception ex)
        {
            _log.LogWarning(ex, "pipe write failed; dropping client");
            DropCurrent();
        }
    }

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
                lock (_gate) { _current = pipe; }

                // Block until either the client disconnects or we're shutting
                // down. We don't read from the pipe — it's one-way.
                while (!stoppingToken.IsCancellationRequested && pipe.IsConnected)
                {
                    try { await Task.Delay(TimeSpan.FromSeconds(1), stoppingToken); }
                    catch (TaskCanceledException) { break; }
                }
            }
            catch (Exception ex) when (ex is not TaskCanceledException)
            {
                _log.LogWarning(ex, "pipe error; recycling");
            }
            finally
            {
                DropCurrent();
                try { await pipe.DisposeAsync(); } catch { /* ignore */ }
            }
        }
    }

    private void DropCurrent()
    {
        lock (_gate)
        {
            _current = null;
        }
    }
}
