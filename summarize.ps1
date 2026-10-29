param([Parameter(Mandatory=$true)][string]$Path)
$ErrorActionPreference = 'Stop'
$rows = @(Get-Content -LiteralPath $Path | ForEach-Object { $_ | ConvertFrom-Json })
$passed = @($rows | Where-Object { $_.success })
Write-Output ('Samples={0} success={1} failed={2}' -f $rows.Count,$passed.Count,($rows.Count-$passed.Count))
if ($passed.Count -eq 0) { return }
foreach ($field in @('queue_ms','tui_detect_ms','clipboard_read_ms','png_encode_ms','ssh_connect_ms','owner_prepare_ms','ssh_transfer_ms','xclip_write_ms','clipboard_verify_ms','key_release_ms','send_input_ms','total_ms')) {
    $values = @($passed | Where-Object { $null -ne $_.PSObject.Properties[$field] } | ForEach-Object { [double]$_.$field } | Sort-Object)
    if ($values.Count -eq 0) { continue }
    $quantile = { param($p) $values[[Math]::Ceiling($values.Count*$p)-1] }
    [pscustomobject]@{
        Stage=$field
        Samples=$values.Count
        Mean=[Math]::Round(($values | Measure-Object -Average).Average,2)
        P50=[Math]::Round((& $quantile 0.50),2)
        P95=[Math]::Round((& $quantile 0.95),2)
        P99=[Math]::Round((& $quantile 0.99),2)
        Max=[Math]::Round($values[-1],2)
    }
}
