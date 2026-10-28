module androidlibbox

go 1.27

require (
	github.com/sagernet/gomobile v0.1.13
	github.com/sagernet/sing-box v1.15.0-alpha.13-reF1nd
)

replace github.com/sagernet/sing-box => github.com/reF1nd/sing-box v1.15.0-alpha.13-reF1nd

// Temporary Xray-compatible fingerprints; applied to staging by reality_compat.py.
replace github.com/metacubex/utls => github.com/metacubex/utls v0.0.0-20261026164304-a6a364afbd13
