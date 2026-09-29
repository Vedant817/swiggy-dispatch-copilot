# Exercises a real pg_dump -> exported artifact -> pg_restore into a disposable
# database, then compares row counts. Requires Docker and a Postgres container.
# This does not replace off-host, encrypted production backups or PITR.
param(
    [Parameter(Mandatory = $true)][string]$Container,
    [string]$Database = "dispatch",
    [string]$User = "dispatch",
    [switch]$Quiesced
)
$ErrorActionPreference = "Stop"
if (-not (Test-Path -LiteralPath $env:TEMP -PathType Container)) { throw "TEMP directory unavailable" }
if ($Database -notmatch '^[A-Za-z_][A-Za-z_0-9]*$' -or $User -notmatch '^[A-Za-z_][A-Za-z_0-9]*$') { throw "Invalid database/user identifier" }
$drill = "dispatch_drill_$([guid]::NewGuid().ToString('N').Substring(0,12))"
$backup = Join-Path $env:TEMP "$drill.dump"
$remote = "/tmp/$drill.dump"
$restoredCopy = "/tmp/$drill.exported.dump"
$created = $false
try {
    & docker exec $Container pg_dump -U $User -d $Database -Fc -f $remote
    if ($LASTEXITCODE -ne 0) { throw "pg_dump failed" }
    & docker cp "${Container}:${remote}" $backup
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $backup)) { throw "backup export failed" }
    # Prove the artifact that left the container can be brought back and used.
    & docker exec $Container rm -f $remote
    if ($LASTEXITCODE -ne 0) { throw "source-side backup removal failed" }
    & docker cp $backup "${Container}:${restoredCopy}"
    if ($LASTEXITCODE -ne 0) { throw "exported backup re-import failed" }
    & docker exec $Container createdb -U $User $drill
    if ($LASTEXITCODE -ne 0) { throw "disposable database create failed" }
    $created = $true
    & docker exec $Container pg_restore -U $User -d $drill --exit-on-error $restoredCopy
    if ($LASTEXITCODE -ne 0) { throw "restore failed" }
    $tables = @('restaurants', 'riders', 'orders', 'assignments', 'proposals', 'webhooks_ledger', 'idempotency_keys', 'assignment_events')
    foreach ($table in $tables) {
        $restored = (& docker exec $Container psql -U $User -d $drill -tAc "SELECT count(*) FROM $table").Trim()
        if ($LASTEXITCODE -ne 0) { throw "restored count failed: $table" }
        if ($Quiesced) {
            # Compare to the source only when applications/workers are stopped:
            # a live source can change after pg_dump's MVCC snapshot.
            $source = (& docker exec $Container psql -U $User -d $Database -tAc "SELECT count(*) FROM $table").Trim()
            if ($LASTEXITCODE -ne 0 -or $source -ne $restored) { throw "restore mismatch: $table source=$source restored=$restored" }
        }
        "$table $restored rows restored"
    }
    "Recovery drill passed: 8 tables restored from exported dump ($((Get-Item -LiteralPath $backup).Length) bytes)."
} finally {
    if ($created) {
        & docker exec $Container dropdb -U $User $drill | Out-Null
        if ($LASTEXITCODE -ne 0) { [Console]::Error.WriteLine("Recovery drill cleanup: failed to drop $drill") }
    }
    & docker exec $Container rm -f $remote $restoredCopy | Out-Null
    if ($LASTEXITCODE -ne 0) { [Console]::Error.WriteLine("Recovery drill cleanup: failed to remove temporary container dumps") }
    if (Test-Path -LiteralPath $backup) { Remove-Item -LiteralPath $backup }
}
