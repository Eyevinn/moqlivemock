package internal

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Dash-Industry-Forum/livesim2/pkg/drm"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
)

var drmSystemIDs = map[string]string{
	"widevine":  "edef8ba9-79d6-4ace-a3c8-27dcd51d21ed",
	"playready": "9a04f079-9840-4286-ab92-e65be0885f95",
	"fairplay":  "94ce86fb-07ff-4f43-adb8-93d2fa968ca2",
}

// DRMInfo keeps track of all information regarding DRM
type DRMInfo struct {
	ContentProtection *ContentProtection
	cenc              *CENCInfo
}

// CENCInfo contains information unique to CENC and is not signaled in the catalog
type CENCInfo struct {
	key []byte
	iv  []byte
}

// MarshalJSON creates a JSON encoding of DRMInfo
func (d *DRMInfo) MarshalJSON() ([]byte, error) {
	if d == nil {
		return []byte("null"), nil
	}
	type debugDRMInfo struct {
		ContentProtection *ContentProtection `json:"contentProtection,omitempty"`
	}
	return json.Marshal(debugDRMInfo{
		ContentProtection: d.ContentProtection,
	})
}

// ConfigureDRMFromFile reads a DRM config file and returns a *DRMInfo struct.
// The config file must be of the same format as assets/testdrm/drm_config_test.json
func ConfigureDRMFromFile(configpath string) (*DRMInfo, error) {
	drmConfig, err := drm.ReadDrmConfig(configpath)
	if err != nil {
		return nil, fmt.Errorf("error reading DRM config: %v", err)
	}
	if len(drmConfig.Packages) == 0 {
		return nil, fmt.Errorf("no packages found in DRM config")
	}
	pack := drmConfig.Packages[0]
	cpix := pack.CPIXData
	drmSystems := make(map[string]DRMSystem)
	for drmName, URL := range pack.URLs {
		if drmName == "fairplay" && URL.CertificateURL == "" {
			return nil, fmt.Errorf("certificate url must be configured for fairplay")
		}
		sysID, ok := drmSystemIDs[drmName]
		if !ok {
			return nil, fmt.Errorf("corresponding systemID for %s not found", drmName)
		}
		drmSystems[sysID] = DRMSystem{
			License: &DRMService{
				URL: URL.LaURL,
			},
			Authorization: &DRMService{
				URL: URL.CertificateURL,
			},
		}
	}
	for _, ds := range cpix.DRMSystems {
		cur, ok := drmSystems[ds.SystemID]
		if !ok {
			return nil, fmt.Errorf("couldn't find existing DRMSystem corresponding to systemID %s", ds.SystemID)
		}
		cur.PSSH = strings.TrimSpace(ds.PSSH)
		drmSystems[ds.SystemID] = cur
	}
	contentProtection := &ContentProtection{
		Scheme:      cpix.ContentKeys[0].CommonEncryptionScheme,
		DefaultKIDs: []string{cpix.ContentKeys[0].KeyID.String()},
		DRMSystems:  drmSystems,
	}

	cenc := &CENCInfo{
		key: cpix.ContentKeys[0].Key,
		iv:  cpix.ContentKeys[0].ExplicitIV,
	}
	return &DRMInfo{
		ContentProtection: contentProtection,
		cenc:              cenc,
	}, nil

}

// ParseCENCflags converts the string CENC-related parameters into a ClearKey-compliant *DRMInfo struct.
// If all flags are empty (except scheme) nil is returned.
func ParseCENCflags(scheme, kidStr, keyStr, ivStr string, fingerprintPort int) (*DRMInfo, error) {
	if kidStr == "" && keyStr == "" && ivStr == "" {
		return nil, nil
	}
	if fingerprintPort <= 0 {
		return nil, fmt.Errorf("invalid or non-configured fingerprintport: %d", fingerprintPort)
	}

	kid, err := mp4.UnpackKey(kidStr)
	if err != nil {
		return nil, fmt.Errorf("invalid key ID %s: %w", kidStr, err)
	}
	kidHex := hex.EncodeToString(kid)
	kidUUID, err := mp4.NewUUIDFromString(kidHex)
	if err != nil {
		return nil, fmt.Errorf("failed to convert kid hexstring to UUID: %w", err)
	}

	if scheme != "cenc" && scheme != "cbcs" {
		return nil, fmt.Errorf("scheme must be cenc or cbcs: %s", scheme)
	}

	if len(ivStr) != 32 && len(ivStr) != 16 {
		return nil, fmt.Errorf("hex iv must have length 16 or 32 chars; %d", len(ivStr))
	}

	iv, err := hex.DecodeString(ivStr)
	if err != nil {
		return nil, fmt.Errorf("invalid iv %s", ivStr)
	}

	if keyStr != "" && len(keyStr) != 32 {
		return nil, fmt.Errorf("hex key must have length 32 chars: %d", len(keyStr))
	}

	var key mp4.UUID
	if keyStr == "" {
		key = kidUUID
	} else {
		key, err = mp4.UnpackKey(keyStr)
		if err != nil {
			return nil, fmt.Errorf("invalid key %s, %w", keyStr, err)
		}
	}
	psshBox, err := createClearKeyPssh(kidUUID)
	if err != nil {
		return nil, fmt.Errorf("could not create ClearKey PSSH: %w", err)
	}

	cenc := &CENCInfo{
		key: key,
		iv:  iv,
	}
	drmSystems := make(map[string]DRMSystem)
	license := &DRMService{
		URL:  fmt.Sprintf("http://localhost:%d/clearkey", fingerprintPort),
		Type: "EME-1.0",
	}
	sw := bits.NewFixedSliceWriter(int(psshBox.Size()))
	err = psshBox.EncodeSW(sw)
	if err != nil {
		return nil, fmt.Errorf("failed to encode pssh box: %w", err)
	}
	drmSystems[psshBox.SystemID.String()] = DRMSystem{
		License: license,
		PSSH:    base64.URLEncoding.EncodeToString(sw.Bytes()),
	}
	contentProtection := &ContentProtection{
		Scheme:      scheme,
		DefaultKIDs: []string{kidUUID.String()},
		DRMSystems:  drmSystems,
	}
	return &DRMInfo{
		ContentProtection: contentProtection,
		cenc:              cenc,
	}, nil
}

// createClearKeyPssh creates a PsshBox using the provided key-id
func createClearKeyPssh(kid mp4.UUID) (*mp4.PsshBox, error) {
	systemID, err := mp4.NewUUIDFromString(CommonSystemID)
	if err != nil {
		return nil, fmt.Errorf("invalid ClearKey system ID: %w", err)
	}

	psshBox := &mp4.PsshBox{
		Version:  1,
		Flags:    0,
		SystemID: systemID,
		KIDs:     []mp4.UUID{kid},
		Data:     nil,
	}

	return psshBox, nil
}
