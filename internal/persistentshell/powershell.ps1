$ErrorActionPreference = 'Continue'
$OutputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
$__rx_pipe = [IO.Pipes.NamedPipeClientStream]::new('.', $env:REASONIX_PWSH_PIPE, [IO.Pipes.PipeDirection]::InOut)
$__rx_pipe.Connect(10000)
$__rx_reader = [IO.BinaryReader]::new($__rx_pipe)
$__rx_writer = [IO.BinaryWriter]::new($__rx_pipe)
function __rx_send($frame) {
  $bytes = [Text.Encoding]::UTF8.GetBytes(($frame | ConvertTo-Json -Compress))
  $__rx_writer.Write([int]$bytes.Length)
  $__rx_writer.Write($bytes)
  $__rx_writer.Flush()
}
__rx_send @{version=1; kind='ready'; id=''}
while ($true) {
  $__rx_size = $__rx_reader.ReadInt32()
  if ($__rx_size -le 0 -or $__rx_size -gt 4194304) { throw 'Invalid control frame size' }
  $__rx_bytes = $__rx_reader.ReadBytes($__rx_size)
  if ($__rx_bytes.Length -ne $__rx_size) { throw 'Truncated control frame' }
  $__rx_request = [Text.Encoding]::UTF8.GetString($__rx_bytes) | ConvertFrom-Json
  if ($__rx_request.version -ne 1 -or $__rx_request.kind -ne 'run') { throw 'Invalid control request' }
  __rx_send @{version=1; kind='started'; id=$__rx_request.id}
  [Console]::Out.Write($__rx_request.start + "`n")
  $LASTEXITCODE = $null
  $__rx_status = 1
  try {
    # Dot sourcing keeps user variables, functions and location in this session.
    # Capture status inside the script, before Out-Default changes $? itself.
    $__rx_script = [scriptblock]::Create($__rx_request.command + "`n" + '$__rx_ok = $?; if (!$__rx_ok -and ($null -eq $LASTEXITCODE -or $LASTEXITCODE -eq 0)) { $__rx_status = 1 } elseif ($null -ne $LASTEXITCODE) { $__rx_status = [int]$LASTEXITCODE } else { $__rx_status = 0 }')
    . $__rx_script 2>&1 | Out-Default
  } catch {
    $__rx_status = 1
    [Console]::Error.WriteLine($_.ToString())
  }
  [Console]::Error.Flush()
  [Console]::Out.Write($__rx_request.end + [string]$__rx_status + "`n")
  [Console]::Out.Flush()
  __rx_send @{version=1; kind='completed'; id=$__rx_request.id; exitCode=$__rx_status}
}
