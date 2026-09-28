# End-to-end demo for Windows (mirrors scripts/demo.sh).
# Requires the API at $Base (start: cd go; go run ./cmd/api).
param([string]$Base = "http://127.0.0.1:8080", [string]$Goal = "reassign stalled VIP order")
$ErrorActionPreference = "Stop"

function Call($Method, $Path, $Body, $Headers) {
    $p = @{ Uri = "$Base$Path"; Method = $Method; TimeoutSec = 15 }
    if ($Body -ne $null) { $p.ContentType = "application/json"; $p.Body = ($Body | ConvertTo-Json -Depth 5) }
    if ($Headers) { $p.Headers = $Headers }
    Invoke-RestMethod @p
}

"== health =="
Call Get /healthz | Out-Null; "ok"
"== reset + seed world =="
Call Delete /admin/reset | Out-Null
Call Post /admin/seed/world @{seed=42; restaurants=5; riders=10} | Format-Table
"== create VIP order =="
$o = Call Post /v1/orders @{restaurant_picker="random"; priority="vip"} @{"Idempotency-Key"=[guid]::NewGuid().ToString()}
$oid = $o.order.id; "OID=$oid"
foreach ($s in @("prepare","ready")) { Call Post "/v1/orders/$oid/$s" @{} | Out-Null }
"== assign (wait for offering) =="
Call Post "/v1/orders/$oid/assign" @{} @{"Idempotency-Key"="assign-$oid"} | Out-Null
for ($i=0; $i -lt 15; $i++) {
    if ((Call Get "/v1/orders/$oid").order.status -eq "offering") { break }
    if ($i -eq 14) { throw "order never reached offering" }
    Start-Sleep -Seconds 1
}
"== snapshot =="
$snap = Call Get /v1/ops/snapshot; "open=$($snap.open_orders) avail=$($snap.available_riders)"
"== rider cancel (webhook) =="
Call Post /v1/webhooks/rider @{event_type="cancelled"; order_id=$oid} @{"Idempotency-Key"="demo-$oid"} | Out-Null; "cancelled"
"== agent plan + auto-commit =="
Push-Location agent
try {
    & python -m app.main --goal $Goal --order $oid --base $Base --auto-confirm
} finally { Pop-Location }
"== final order (must be assigned) =="
$final = Call Get "/v1/orders/$oid"
$final.order | Select-Object id,status
if ($final.order.status -ne "assigned") { throw "demo FAILED: $($final.order.status)" }
"demo done: $oid assigned"
