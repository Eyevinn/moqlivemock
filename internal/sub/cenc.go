package sub

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/mp4ff/mp4"
)

// CENC holds decryption state for encrypted tracks.
type CENC struct {
	Key         []byte
	DecryptInfo map[string]mp4.DecryptInfo // keyed by track name
}

type clearKeyRequest struct {
	Kids []string `json:"kids"`
}

type keyInfo struct {
	Kty string `json:"kty"`
	K   string `json:"k"`
	Kid string `json:"kid"`
}

type clearKeyResponse struct {
	Keys []keyInfo `json:"keys"`
}

// requestClearKey makes a POST request to a ClearKey server and returns the response.
func requestClearKey(laurl string, kids []string) ([]keyInfo, error) {
	slog.Info("requesting clearkey license")
	reqBody, err := json.Marshal(clearKeyRequest{Kids: kids})
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(laurl, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ClearKey request failed: %s", resp.Status)
	}

	var ckResp clearKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&ckResp); err != nil {
		return nil, err
	}
	return ckResp.Keys, nil
}

// decryptInit decrypts the init data, requests the ClearKey license server
// and stores protection information in the Handler.
func (h *Handler) decryptInit(track internal.Track) error {
	initDataBytes, err := base64.StdEncoding.DecodeString(track.InitData)
	if err != nil {
		return fmt.Errorf("failed to base64 decode init data: %w", err)
	}
	decryptedInitBytes, _, decryptInfo, err := internal.DecryptInit(initDataBytes)
	if err != nil {
		return fmt.Errorf("failed to decrypt init: %w", err)
	}
	var clearKeyRefID string
	for i, id := range track.ContentProtectionRefIDs {
		if h.catalog.ContentProtections[i].DRMSystem.SystemID == internal.CommonSystemID {
			clearKeyRefID = id
		}
	}
	if clearKeyRefID == "" {
		return fmt.Errorf("ClearKey not supported for track")
	}
	err = h.setClearKeyDecryptionKey(clearKeyRefID)
	if err != nil {
		return fmt.Errorf("failed to set ClearKey decryption key: %w", err)
	}

	decryptedInit := base64.StdEncoding.EncodeToString(decryptedInitBytes)
	h.cenc.DecryptInfo[track.Name] = decryptInfo
	track.InitData = decryptedInit
	return nil
}

func (h *Handler) decryptPayload(payload []byte, trackName string) ([]byte, error) {
	return internal.DecryptFragment(payload, h.cenc.DecryptInfo[trackName], h.cenc.Key)
}

// setClearKeyDecryptionKey parses the ContentProtections array and makes a ClearKey request.
func (h *Handler) setClearKeyDecryptionKey(refID string) error {
	var clearKeyProtection internal.ContentProtection
	for _, cp := range h.catalog.ContentProtections {
		if refID == cp.RefID {
			clearKeyProtection = cp
		}
	}
	var kids []string
	for _, uuid := range clearKeyProtection.DefaultKIDs {
		kid, err := mp4.UnpackKey(uuid)
		if err != nil {
			return fmt.Errorf("failed to unpack kid UUID: %w", err)
		}
		kids = append(kids, base64.RawURLEncoding.EncodeToString(kid))
	}
	if clearKeyProtection.RefID == "" {
		return fmt.Errorf("failed to find contentProtection with refID %s", refID)
	}
	keys, err := requestClearKey(clearKeyProtection.DRMSystem.LaURL.URL, kids)
	if err != nil {
		return fmt.Errorf("failed to fetch ClearKey: %w", err)
	}
	if len(keys) == 0 {
		return fmt.Errorf("no keys found in ClearKey response")
	}
	if len(keys) > 1 {
		return fmt.Errorf("too many keys in ClearKey response: got %d, want 1", len(keys))
	}
	key, err := base64.RawURLEncoding.DecodeString(keys[0].K)
	if err != nil {
		return fmt.Errorf("unable to base64URL-decode key in ClearKey response: %w", err)
	}
	h.cenc.Key = key
	return nil
}
