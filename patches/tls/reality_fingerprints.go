//go:build with_utls

// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// SPDX-License-Identifier: MPL-2.0
//
// Adapted from XTLS/Xray-core v26.10.21, transport/internet/tls/tls.go:
// https://github.com/XTLS/Xray-core/blob/1b1647cd9ddf0abc34b981d4611fc03bcac1ca74/transport/internet/tls/tls.go
// Names are normalized as in that revision's infra/conf/transport_security.go.
// Local adaptations: MetaCubeX uTLS imports, package-local names, sing-box's
// legacy Chrome aliases, and an error for unknown or non-uTLS selections.
// This file is injected into the staged core; remove it and its entry-point
// patch when the pinned core provides these mappings itself.

package tls

import (
	"crypto/rand"
	"math/big"
	"strings"

	utls "github.com/metacubex/utls"
	E "github.com/sagernet/sing/common/exceptions"
)

// Match Xray's process-wide random selection and seeded randomized profiles.
func init() {
	bigInt, _ := rand.Int(rand.Reader, big.NewInt(int64(len(androidLibBoxModernFingerprints))))
	stopAt := int(bigInt.Int64())
	i := 0
	for _, v := range androidLibBoxModernFingerprints {
		if i == stopAt {
			androidLibBoxPresetFingerprints["random"] = v
			break
		}
		i++
	}
	weights := utls.DefaultWeights
	weights.TLSVersMax_Set_VersionTLS13 = 1
	weights.FirstKeyShare_Set_CurveP256 = 0
	randomized := utls.HelloRandomizedALPN
	randomized.Seed, _ = utls.NewPRNGSeed()
	randomized.Weights = &weights
	randomizednoalpn := utls.HelloRandomizedNoALPN
	randomizednoalpn.Seed, _ = utls.NewPRNGSeed()
	randomizednoalpn.Weights = &weights
	androidLibBoxPresetFingerprints["randomized"] = &randomized
	androidLibBoxPresetFingerprints["randomizednoalpn"] = &randomizednoalpn
}

func androidLibBoxClientHelloID(name string) (utls.ClientHelloID, error) {
	// Xray normalizes configuration names, but does not trim whitespace.
	name = strings.ToLower(name)
	switch name {
	case "":
		return utls.HelloChrome_Auto, nil
	case "chrome_psk", "chrome_psk_shuffle", "chrome_padding_psk_shuffle", "chrome_pq", "chrome_pq_psk":
		// Preserve sing-box's existing aliases, which already resolve to Chrome Auto.
		name = "chrome"
	}
	if id := androidLibBoxPresetFingerprints[name]; id != nil {
		return *id, nil
	}
	if id := androidLibBoxModernFingerprints[name]; id != nil {
		return *id, nil
	}
	if id := androidLibBoxOtherFingerprints[name]; id != nil {
		return *id, nil
	}
	return utls.ClientHelloID{}, E.New("unknown uTLS fingerprint: ", name)
}

var androidLibBoxPresetFingerprints = map[string]*utls.ClientHelloID{
	// Recommended preset options in GUI clients
	"chrome":           &utls.HelloChrome_Auto,
	"firefox":          &utls.HelloFirefox_Auto,
	"safari":           &utls.HelloSafari_Auto,
	"ios":              &utls.HelloIOS_Auto,
	"android":          &utls.HelloAndroid_11_OkHttp,
	"edge":             &utls.HelloEdge_Auto,
	"360":              &utls.Hello360_Auto,
	"qq":               &utls.HelloQQ_Auto,
	"random":           nil,
	"randomized":       nil,
	"randomizednoalpn": nil,
	// Xray uses unsafe to bypass uTLS for ordinary TLS; it is not a fingerprint.
	// Keep it unavailable here: this entry point always creates a uTLS client.
	"unsafe": nil,
}

var androidLibBoxModernFingerprints = map[string]*utls.ClientHelloID{
	// One of these will be chosen as `random` at startup
	"hellofirefox_120": &utls.HelloFirefox_120,
	"hellofirefox_148": &utls.HelloFirefox_148,
	"hellochrome_120":  &utls.HelloChrome_120,
	"hellochrome_131":  &utls.HelloChrome_131,
	"hellochrome_133":  &utls.HelloChrome_133,
	"helloios_13":      &utls.HelloIOS_13,
	"helloios_14":      &utls.HelloIOS_14,
	"helloedge_106":    &utls.HelloEdge_106,
	"hellosafari_26_3": &utls.HelloSafari_26_3,
	"hello360_11_0":    &utls.Hello360_11_0,
	"helloqq_11_1":     &utls.HelloQQ_11_1,
}

var androidLibBoxOtherFingerprints = map[string]*utls.ClientHelloID{
	// Golang, randomized, auto, and fingerprints that are too old
	"hellogolang":             &utls.HelloGolang,
	"hellorandomized":         &utls.HelloRandomized,
	"hellorandomizedalpn":     &utls.HelloRandomizedALPN,
	"hellorandomizednoalpn":   &utls.HelloRandomizedNoALPN,
	"hellofirefox_auto":       &utls.HelloFirefox_Auto,
	"hellofirefox_55":         &utls.HelloFirefox_55,
	"hellofirefox_56":         &utls.HelloFirefox_56,
	"hellofirefox_63":         &utls.HelloFirefox_63,
	"hellofirefox_65":         &utls.HelloFirefox_65,
	"hellofirefox_99":         &utls.HelloFirefox_99,
	"hellofirefox_102":        &utls.HelloFirefox_102,
	"hellofirefox_105":        &utls.HelloFirefox_105,
	"hellochrome_auto":        &utls.HelloChrome_Auto,
	"hellochrome_58":          &utls.HelloChrome_58,
	"hellochrome_62":          &utls.HelloChrome_62,
	"hellochrome_70":          &utls.HelloChrome_70,
	"hellochrome_72":          &utls.HelloChrome_72,
	"hellochrome_83":          &utls.HelloChrome_83,
	"hellochrome_87":          &utls.HelloChrome_87,
	"hellochrome_96":          &utls.HelloChrome_96,
	"hellochrome_100":         &utls.HelloChrome_100,
	"hellochrome_102":         &utls.HelloChrome_102,
	"hellochrome_106_shuffle": &utls.HelloChrome_106_Shuffle,
	"helloios_auto":           &utls.HelloIOS_Auto,
	"helloios_11_1":           &utls.HelloIOS_11_1,
	"helloios_12_1":           &utls.HelloIOS_12_1,
	"helloandroid_11_okhttp":  &utls.HelloAndroid_11_OkHttp,
	"helloedge_85":            &utls.HelloEdge_85,
	"helloedge_auto":          &utls.HelloEdge_Auto,
	"hellosafari_16_0":        &utls.HelloSafari_16_0,
	"hellosafari_auto":        &utls.HelloSafari_Auto,
	"hello360_auto":           &utls.Hello360_Auto,
	"hello360_7_5":            &utls.Hello360_7_5,
	"helloqq_auto":            &utls.HelloQQ_Auto,

	// Chrome betas
	"hellochrome_100_psk":              &utls.HelloChrome_100_PSK,
	"hellochrome_112_psk_shuf":         &utls.HelloChrome_112_PSK_Shuf,
	"hellochrome_114_padding_psk_shuf": &utls.HelloChrome_114_Padding_PSK_Shuf,
	"hellochrome_115_pq":               &utls.HelloChrome_115_PQ,
	"hellochrome_115_pq_psk":           &utls.HelloChrome_115_PQ_PSK,
	"hellochrome_120_pq":               &utls.HelloChrome_120_PQ,
}
