param(
    [string]$ContainerName = 'hajimi-payment-dev-postgres-1'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$migrationFile = Join-Path $projectRoot 'migrations/000001_payment_orders.up.sql'
if (-not (Test-Path -LiteralPath $migrationFile)) { throw 'Initial migration file is missing' }

$labelsJSON = docker inspect --format '{{json .Config.Labels}}' $ContainerName
if ($LASTEXITCODE -ne 0) { throw 'Start the payment development PostgreSQL container first' }
$labels = ConvertFrom-Json -InputObject $labelsJSON
if ($labels.'com.docker.compose.project' -ne 'hajimi-payment-dev' -or $labels.'com.docker.compose.service' -ne 'postgres') {
    throw 'This script only targets the named payment development PostgreSQL service'
}
$portsJSON = docker inspect --format '{{json .NetworkSettings.Ports}}' $ContainerName
$ports = ConvertFrom-Json -InputObject $portsJSON
$loopback = @($ports.'5432/tcp' | Where-Object { $_.HostIp -eq '127.0.0.1' -and $_.HostPort -eq '55432' })
if ($loopback.Count -ne 1) { throw 'Expected PostgreSQL at 127.0.0.1:55432' }

$adminPassword = $env:PAYMENT_DB_ADMIN_PASSWORD
if ([string]::IsNullOrEmpty($adminPassword)) {
    $securePassword = Read-Host 'Local PostgreSQL postgres password (test database only)' -AsSecureString
    $credential = [System.Management.Automation.PSCredential]::new('postgres', $securePassword)
    $adminPassword = $credential.GetNetworkCredential().Password
}
if ([string]::IsNullOrEmpty($adminPassword)) { throw 'Database administration password is required for integration tests' }

$testDatabase = 'payment_it_' + [guid]::NewGuid().ToString('N').Substring(0, 12)
if ($testDatabase -notmatch '^payment_it_[0-9a-f]{12}$') { throw 'Invalid generated test database name' }
$databaseCreated = $false
$oldPassword = $env:PGPASSWORD
$oldTestDSN = $env:PAYMENT_TEST_DATABASE_URL
Push-Location -LiteralPath $projectRoot
try {
    docker exec $ContainerName psql -X -U postgres -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE $testDatabase TEMPLATE template0;"
    if ($LASTEXITCODE -ne 0) { throw 'Could not create isolated integration database' }
    $databaseCreated = $true

    $migrationSQL = [System.IO.File]::ReadAllText($migrationFile, [System.Text.Encoding]::UTF8)
    $migrationSQL | docker exec -i $ContainerName psql -X -U postgres -d $testDatabase -v ON_ERROR_STOP=1
    if ($LASTEXITCODE -ne 0) { throw 'Could not create integration test schema' }

    $env:PGPASSWORD = $adminPassword
    $env:PAYMENT_TEST_DATABASE_URL = "postgres://postgres@127.0.0.1:55432/${testDatabase}?sslmode=disable"
    go test -tags=integration -count=1 ./...
    if ($LASTEXITCODE -ne 0) { throw 'PostgreSQL integration tests failed' }
} finally {
    $env:PGPASSWORD = $oldPassword
    $env:PAYMENT_TEST_DATABASE_URL = $oldTestDSN
    try {
        if ($databaseCreated) {
            docker exec $ContainerName psql -X -U postgres -d postgres -v ON_ERROR_STOP=1 -c "DROP DATABASE $testDatabase;"
            if ($LASTEXITCODE -ne 0) { throw "Could not remove temporary test database $testDatabase" }
        }
    } finally {
        Pop-Location
    }
}
