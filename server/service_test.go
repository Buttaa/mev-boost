package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flashbots/go-boost-utils/bls"
	"github.com/flashbots/mev-boost/common"
	"github.com/flashbots/mev-boost/proposerconfig"
	"github.com/flashbots/mev-boost/testutils"

	"github.com/flashbots/go-boost-utils/types"
	"github.com/stretchr/testify/require"
)

type testBackend struct {
	boost  *BoostService
	relays []*testutils.MockRelay
}

// newTestBackend creates a new backend, initializes mock relays, registers them and return the instance
func newTestBackend(t *testing.T, numRelays int, relayTimeout time.Duration) *testBackend {
	backend := testBackend{
		relays: make([]*testutils.MockRelay, numRelays),
	}

	relayEntries := make([]common.RelayEntry, numRelays)
	for i := 0; i < numRelays; i++ {
		// Create a mock relay
		backend.relays[i] = testutils.NewMockRelay(t)
		relayEntries[i] = backend.relays[i].RelayEntry
	}

	opts := BoostServiceOpts{
		Log:        testutils.TestLog,
		ListenAddr: "localhost:12345",
		PCS: &proposerconfig.ProposerConfigurationStorage{
			ProposerConfigurations: map[types.PublicKey]*proposerconfig.ProposerConfig{},
			DefaultConfiguration: &proposerconfig.ProposerConfig{
				Relays: relayEntries,
			},
		},
		GenesisForkVersionHex: "0x00000000",
		RelayRequestTimeout:   relayTimeout,
		RelayCheck:            true,
	}
	service, err := NewBoostService(opts)
	require.NoError(t, err)

	backend.boost = service
	return &backend
}

func (be *testBackend) request(t *testing.T, method string, path string, payload any) *httptest.ResponseRecorder {
	var req *http.Request
	var err error

	if payload == nil {
		req, err = http.NewRequest(method, path, bytes.NewReader(nil))
	} else {
		payloadBytes, err2 := json.Marshal(payload)
		require.NoError(t, err2)
		req, err = http.NewRequest(method, path, bytes.NewReader(payloadBytes))
	}

	require.NoError(t, err)
	rr := httptest.NewRecorder()
	be.boost.getRouter().ServeHTTP(rr, req)
	return rr
}

func newKeyPair(t *testing.T) (*bls.SecretKey, *types.PublicKey) {
	blsPrivateKey, blsPublicKey, err := bls.GenerateNewKeypair()
	require.NoError(t, err)

	publicKey := &types.PublicKey{}
	publicKey.FromSlice(blsPublicKey.Compress())

	return blsPrivateKey, publicKey
}

func newGetHeaderPath(slot uint64, parentHash types.Hash, pubkey types.PublicKey) string {
	return fmt.Sprintf("/eth/v1/builder/header/%d/%s/%s", slot, parentHash.String(), pubkey.String())
}

func newPayload(t *testing.T, secretKey *bls.SecretKey, slot uint64, parentHash, blockHash types.Hash) types.SignedBlindedBeaconBlock {
	message := &types.BlindedBeaconBlock{
		Slot:          slot,
		ProposerIndex: 1,
		ParentRoot:    types.Root{0x01},
		StateRoot:     types.Root{0x02},
		Body: &types.BlindedBeaconBlockBody{
			RandaoReveal:  types.Signature{0xa1},
			Eth1Data:      &types.Eth1Data{},
			Graffiti:      types.Hash{0xa2},
			SyncAggregate: &types.SyncAggregate{},
			ExecutionPayloadHeader: &types.ExecutionPayloadHeader{
				ParentHash:   parentHash,
				BlockHash:    blockHash,
				BlockNumber:  12345,
				FeeRecipient: testutils.HexToAddressP("0xdb65fEd33dc262Fe09D9a2Ba8F80b329BA25f941"),
			},
		},
	}

	signature, err := types.SignMessage(message, types.DomainBuilder, secretKey)
	require.NoError(t, err)

	return types.SignedBlindedBeaconBlock{
		Signature: signature,
		Message:   message,
	}
}

func TestNewBoostServiceErrors(t *testing.T) {
	t.Run("errors when no relays", func(t *testing.T) {
		_, err := NewBoostService(BoostServiceOpts{testutils.TestLog, ":123", "0x00000000", time.Second, true, &proposerconfig.ProposerConfigurationStorage{
			DefaultConfiguration: &proposerconfig.ProposerConfig{
				Relays: []common.RelayEntry{},
			},
		}})
		require.Error(t, err)
	})
}

func TestWebserver(t *testing.T) {
	t.Run("errors when webserver is already existing", func(t *testing.T) {
		backend := newTestBackend(t, 1, time.Second)
		backend.boost.srv = &http.Server{}
		err := backend.boost.StartHTTPServer()
		require.Error(t, err)
	})

	t.Run("webserver error on invalid listenAddr", func(t *testing.T) {
		backend := newTestBackend(t, 1, time.Second)
		backend.boost.listenAddr = "localhost:876543"
		err := backend.boost.StartHTTPServer()
		require.Error(t, err)
	})

	// t.Run("webserver starts normally", func(t *testing.T) {
	// 	backend := newTestBackend(t, 1, time.Second)
	// 	go func() {
	// 		err := backend.boost.StartHTTPServer()
	// 		require.NoError(t, err)
	// 	}()
	// 	time.Sleep(time.Millisecond * 100)
	// 	backend.boost.srv.Close()
	// })
}

func TestWebserverRootHandler(t *testing.T) {
	backend := newTestBackend(t, 1, time.Second)

	// Check root handler
	req, _ := http.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	backend.boost.getRouter().ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "{}\n", rr.Body.String())
}

func TestWebserverMaxHeaderSize(t *testing.T) {
	backend := newTestBackend(t, 1, time.Second)
	addr := "localhost:1234"
	backend.boost.listenAddr = addr
	go func() {
		err := backend.boost.StartHTTPServer()
		require.NoError(t, err)
	}()
	time.Sleep(time.Millisecond * 100)
	path := "http://" + addr + "?" + strings.Repeat("abc", 4000) // path with characters of size over 4kb
	code, err := common.SendHTTPRequest(context.Background(), *http.DefaultClient, http.MethodGet, path, "test", nil, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusRequestHeaderFieldsTooLarge, code)
	backend.boost.srv.Close()
}

// Example good registerValidator payload
var payloadRegisterValidator = types.SignedValidatorRegistration{
	Message: &types.RegisterValidatorRequestMessage{
		FeeRecipient: testutils.HexToAddressP("0xdb65fEd33dc262Fe09D9a2Ba8F80b329BA25f941"),
		Timestamp:    1234356,
		GasLimit:     278234191203,
		Pubkey: testutils.HexToPubkeyP(
			"0x8a1d7b8dd64e0aafe7ea7b6c95065c9364cf99d38470c12ee807d55f7de1529ad29ce2c422e0b65e3d5a05c02caca249"),
	},
	// Signed by 0x4e343a647c5a5c44d76c2c58b63f02cdf3a9a0ec40f102ebc26363b4b1b95033
	Signature: testutils.HexToSignatureP(
		"0x8209b5391cd69f392b1f02dbc03bab61f574bb6bb54bf87b59e2a85bdc0756f7db6a71ce1b41b727a1f46ccc77b213bf0df1426177b5b29926b39956114421eaa36ec4602969f6f6370a44de44a6bce6dae2136e5fb594cce2a476354264d1ea"),
}

func TestStatus(t *testing.T) {
	t.Run("At least one relay is available", func(t *testing.T) {
		backend := newTestBackend(t, 2, time.Second)
		path := "/eth/v1/builder/status"
		rr := backend.request(t, http.MethodGet, path, payloadRegisterValidator)

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, 1, backend.relays[0].GetRequestCount(path))
	})

	t.Run("No relays available", func(t *testing.T) {
		backend := newTestBackend(t, 1, time.Second)

		// Make the relay unavailable.
		backend.relays[0].Server.Close()

		path := "/eth/v1/builder/status"
		rr := backend.request(t, http.MethodGet, path, payloadRegisterValidator)

		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
		require.Equal(t, 0, backend.relays[0].GetRequestCount(path))
	})
}

func TestRegisterValidator(t *testing.T) {
	path := "/eth/v1/builder/validators"
	reg := types.SignedValidatorRegistration{
		Message: &types.RegisterValidatorRequestMessage{
			FeeRecipient: testutils.HexToAddressP("0xdb65fEd33dc262Fe09D9a2Ba8F80b329BA25f941"),
			Timestamp:    1234356,
			Pubkey: testutils.HexToPubkeyP(
				"0x8a1d7b8dd64e0aafe7ea7b6c95065c9364cf99d38470c12ee807d55f7de1529ad29ce2c422e0b65e3d5a05c02caca249"),
		},
		Signature: testutils.HexToSignatureP(
			"0x81510b571e22f89d1697545aac01c9ad0c1e7a3e778b3078bef524efae14990e58a6e960a152abd49de2e18d7fd3081c15d5c25867ccfad3d47beef6b39ac24b6b9fbf2cfa91c88f67aff750438a6841ec9e4a06a94ae41410c4f97b75ab284c"),
	}
	payload := []types.SignedValidatorRegistration{reg}

	t.Run("Normal function", func(t *testing.T) {
		backend := newTestBackend(t, 1, time.Second)
		rr := backend.request(t, http.MethodPost, path, payload)
		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, 1, backend.relays[0].GetRequestCount(path))
	})

	t.Run("Relay error response", func(t *testing.T) {
		backend := newTestBackend(t, 2, time.Second)

		backend.relays[0].ResponseDelay = 5 * time.Millisecond
		backend.relays[1].ResponseDelay = 5 * time.Millisecond

		rr := backend.request(t, http.MethodPost, path, payload)
		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, 1, backend.relays[0].GetRequestCount(path))
		require.Equal(t, 1, backend.relays[1].GetRequestCount(path))

		// Now make one relay return an error
		backend.relays[0].OverrideHandleRegisterValidator(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		})
		rr = backend.request(t, http.MethodPost, path, payload)
		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, 2, backend.relays[0].GetRequestCount(path))
		require.Equal(t, 2, backend.relays[1].GetRequestCount(path))

		// Now make both relays return an error - which should cause the request to fail
		backend.relays[1].OverrideHandleRegisterValidator(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		})
		rr = backend.request(t, http.MethodPost, path, payload)
		require.Equal(t, `{"code":502,"message":"no successful relay response"}`+"\n", rr.Body.String())
		require.Equal(t, http.StatusBadGateway, rr.Code)
		require.Equal(t, 3, backend.relays[0].GetRequestCount(path))
		require.Equal(t, 3, backend.relays[1].GetRequestCount(path))
	})

	t.Run("mev-boost relay timeout works with slow relay", func(t *testing.T) {
		backend := newTestBackend(t, 1, 5*time.Millisecond) // 10ms max
		rr := backend.request(t, http.MethodPost, path, payload)
		require.Equal(t, http.StatusOK, rr.Code)

		// Now make the relay return slowly, mev-boost should return an error
		backend.relays[0].ResponseDelay = 10 * time.Millisecond
		rr = backend.request(t, http.MethodPost, path, payload)
		require.Equal(t, `{"code":502,"message":"no successful relay response"}`+"\n", rr.Body.String())
		require.Equal(t, http.StatusBadGateway, rr.Code)
		require.Equal(t, 2, backend.relays[0].GetRequestCount(path))
	})
}

func TestGetHeader(t *testing.T) {
	hash := testutils.HexToHashP("0xe28385e7bd68df656cd0042b74b69c3104b5356ed1f20eb69f1f925df47a3ab7")
	pubkey := testutils.HexToPubkeyP(
		"0x8a1d7b8dd64e0aafe7ea7b6c95065c9364cf99d38470c12ee807d55f7de1529ad29ce2c422e0b65e3d5a05c02caca249")
	path := newGetHeaderPath(1, hash, pubkey)
	require.Equal(t, "/eth/v1/builder/header/1/0xe28385e7bd68df656cd0042b74b69c3104b5356ed1f20eb69f1f925df47a3ab7/0x8a1d7b8dd64e0aafe7ea7b6c95065c9364cf99d38470c12ee807d55f7de1529ad29ce2c422e0b65e3d5a05c02caca249", path)

	t.Run("Okay response from relay", func(t *testing.T) {
		backend := newTestBackend(t, 1, time.Second)
		rr := backend.request(t, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, 1, backend.relays[0].GetRequestCount(path))
	})

	t.Run("Bad response from relays", func(t *testing.T) {
		backend := newTestBackend(t, 2, time.Second)
		resp := backend.relays[0].MakeGetHeaderResponse(
			12345,
			"0xe28385e7bd68df656cd0042b74b69c3104b5356ed1f20eb69f1f925df47a3ab7",
			"0x8a1d7b8dd64e0aafe7ea7b6c95065c9364cf99d38470c12ee807d55f7de1529ad29ce2c422e0b65e3d5a05c02caca249",
		)
		resp.Data.Message.Header.BlockHash = nilHash

		// 1/2 failing responses are okay
		backend.relays[0].GetHeaderResponse = resp
		rr := backend.request(t, http.MethodGet, path, nil)
		require.Equal(t, 1, backend.relays[0].GetRequestCount(path))
		require.Equal(t, 1, backend.relays[1].GetRequestCount(path))
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

		// 2/2 failing responses are okay
		backend.relays[1].GetHeaderResponse = resp
		rr = backend.request(t, http.MethodGet, path, nil)
		require.Equal(t, 2, backend.relays[0].GetRequestCount(path))
		require.Equal(t, 2, backend.relays[1].GetRequestCount(path))
		require.Equal(t, http.StatusNoContent, rr.Code)
	})

	t.Run("Use header with highest value", func(t *testing.T) {
		// Create backend and register 3 relays.
		backend := newTestBackend(t, 3, time.Second)

		// First relay will return signed response with value 12345.
		backend.relays[0].GetHeaderResponse = backend.relays[0].MakeGetHeaderResponse(
			12345,
			"0xe28385e7bd68df656cd0042b74b69c3104b5356ed1f20eb69f1f925df47a3ab7",
			"0x8a1d7b8dd64e0aafe7ea7b6c95065c9364cf99d38470c12ee807d55f7de1529ad29ce2c422e0b65e3d5a05c02caca249",
		)

		// First relay will return signed response with value 12347.
		backend.relays[1].GetHeaderResponse = backend.relays[1].MakeGetHeaderResponse(
			12347,
			"0xe28385e7bd68df656cd0042b74b69c3104b5356ed1f20eb69f1f925df47a3ab7",
			"0x8a1d7b8dd64e0aafe7ea7b6c95065c9364cf99d38470c12ee807d55f7de1529ad29ce2c422e0b65e3d5a05c02caca249",
		)

		// First relay will return signed response with value 12346.
		backend.relays[2].GetHeaderResponse = backend.relays[2].MakeGetHeaderResponse(
			12346,
			"0xe28385e7bd68df656cd0042b74b69c3104b5356ed1f20eb69f1f925df47a3ab7",
			"0x8a1d7b8dd64e0aafe7ea7b6c95065c9364cf99d38470c12ee807d55f7de1529ad29ce2c422e0b65e3d5a05c02caca249",
		)

		// Run the request.
		rr := backend.request(t, http.MethodGet, path, nil)

		// Each relay must have received the request.
		require.Equal(t, 1, backend.relays[0].GetRequestCount(path))
		require.Equal(t, 1, backend.relays[1].GetRequestCount(path))
		require.Equal(t, 1, backend.relays[2].GetRequestCount(path))

		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

		// Highest value should be 12347, i.e. second relay.
		resp := new(types.GetHeaderResponse)
		err := json.Unmarshal(rr.Body.Bytes(), resp)
		require.NoError(t, err)
		require.Equal(t, types.IntToU256(12347), resp.Data.Message.Value)
	})

	t.Run("Invalid relay public key", func(t *testing.T) {
		backend := newTestBackend(t, 1, time.Second)

		backend.relays[0].GetHeaderResponse = backend.relays[0].MakeGetHeaderResponse(
			12345,
			"0xe28385e7bd68df656cd0042b74b69c3104b5356ed1f20eb69f1f925df47a3ab7",
			"0x8a1d7b8dd64e0aafe7ea7b6c95065c9364cf99d38470c12ee807d55f7de1529ad29ce2c422e0b65e3d5a05c02caca249",
		)

		// Simulate a different public key registered to mev-boost
		pk := types.PublicKey{}
		backend.boost.pcs.DefaultConfiguration.Relays[0].PublicKey = pk

		rr := backend.request(t, http.MethodGet, path, nil)
		require.Equal(t, 1, backend.relays[0].GetRequestCount(path))

		// Request should have no content
		require.Equal(t, http.StatusNoContent, rr.Code)
	})

	t.Run("Invalid relay signature", func(t *testing.T) {
		backend := newTestBackend(t, 1, time.Second)

		backend.relays[0].GetHeaderResponse = backend.relays[0].MakeGetHeaderResponse(
			12345,
			"0xe28385e7bd68df656cd0042b74b69c3104b5356ed1f20eb69f1f925df47a3ab7",
			"0x8a1d7b8dd64e0aafe7ea7b6c95065c9364cf99d38470c12ee807d55f7de1529ad29ce2c422e0b65e3d5a05c02caca249",
		)

		// Scramble the signature
		backend.relays[0].GetHeaderResponse.Data.Signature = types.Signature{}

		rr := backend.request(t, http.MethodGet, path, nil)
		require.Equal(t, 1, backend.relays[0].GetRequestCount(path))

		// Request should have no content
		require.Equal(t, http.StatusNoContent, rr.Code)
	})

	t.Run("Invalid slot number", func(t *testing.T) {
		// Number larger than uint64 creates parsing error
		slot := fmt.Sprintf("%d0", uint64(math.MaxUint64))
		invalidSlotPath := fmt.Sprintf("/eth/v1/builder/header/%s/%s/%s", slot, hash.String(), pubkey.String())

		backend := newTestBackend(t, 1, time.Second)
		rr := backend.request(t, http.MethodGet, invalidSlotPath, nil)
		require.Equal(t, `{"code":400,"message":"invalid slot"}`+"\n", rr.Body.String())
		require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
		require.Equal(t, 0, backend.relays[0].GetRequestCount(path))
	})

	t.Run("Invalid pubkey length", func(t *testing.T) {
		invalidPubkeyPath := fmt.Sprintf("/eth/v1/builder/header/%d/%s/%s", 1, hash.String(), "0x1")

		backend := newTestBackend(t, 1, time.Second)
		rr := backend.request(t, http.MethodGet, invalidPubkeyPath, nil)
		require.Equal(t, `{"code":400,"message":"invalid pubkey"}`+"\n", rr.Body.String())
		require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
		require.Equal(t, 0, backend.relays[0].GetRequestCount(path))
	})

	t.Run("Invalid hash length", func(t *testing.T) {
		invalidSlotPath := fmt.Sprintf("/eth/v1/builder/header/%d/%s/%s", 1, "0x1", pubkey.String())

		backend := newTestBackend(t, 1, time.Second)
		rr := backend.request(t, http.MethodGet, invalidSlotPath, nil)
		require.Equal(t, `{"code":400,"message":"invalid hash"}`+"\n", rr.Body.String())
		require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
		require.Equal(t, 0, backend.relays[0].GetRequestCount(path))
	})

	t.Run("Invalid parent hash", func(t *testing.T) {
		backend := newTestBackend(t, 1, time.Second)

		invalidParentHashPath := newGetHeaderPath(1, types.Hash{}, pubkey)
		rr := backend.request(t, http.MethodGet, invalidParentHashPath, nil)
		require.Equal(t, http.StatusNoContent, rr.Code)
		require.Equal(t, 0, backend.relays[0].GetRequestCount(path))
	})

	t.Run("Request specific relay based on proposer", func(t *testing.T) {
		// Initiate three proposers.
		_, proposer1 := newKeyPair(t)
		_, proposer2 := newKeyPair(t)
		_, proposer3 := newKeyPair(t)

		// Create a backend with 2 relays.
		// Update the handler to have two distinct routes with one more profitable than the other.
		backend := newTestBackend(t, 2, time.Second)
		backend.relays[1].GetHeaderResponse = backend.relays[1].MakeGetHeaderResponse(
			1,
			"0xe28385e7bd68df656cd0042b74b69c3104b5356ed1f20eb69f1f925df47a3ab7",
			"0x8a1d7b8dd64e0aafe7ea7b6c95065c9364cf99d38470c12ee807d55f7de1529ad29ce2c422e0b65e3d5a05c02caca249",
		)
		backend.relays[1].GetHeaderResponse = backend.relays[1].MakeGetHeaderResponse(
			2,
			"0xe28385e7bd68df656cd0042b74b69c3104b5356ed1f20eb69f1f925df47a3ab7",
			"0x8a1d7b8dd64e0aafe7ea7b6c95065c9364cf99d38470c12ee807d55f7de1529ad29ce2c422e0b65e3d5a05c02caca249",
		)

		// Bind the first proposer to the first relay only.
		backend.boost.pcs.ProposerConfigurations[*proposer1] = &proposerconfig.ProposerConfig{
			Relays: []common.RelayEntry{
				backend.relays[0].RelayEntry,
			},
		}
		// Same goes for second proposer and second relay only.
		backend.boost.pcs.ProposerConfigurations[*proposer2] = &proposerconfig.ProposerConfig{
			Relays: []common.RelayEntry{
				backend.relays[1].RelayEntry,
			},
		}

		pathProposer1 := newGetHeaderPath(1, hash, *proposer1)
		rr := backend.request(t, http.MethodGet, pathProposer1, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, 1, backend.relays[0].GetRequestCount(pathProposer1))

		pathProposer2 := newGetHeaderPath(1, hash, *proposer2)
		rr = backend.request(t, http.MethodGet, pathProposer2, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, 1, backend.relays[1].GetRequestCount(pathProposer2))

		// Proposer 3 should have made request to both relays.
		pathProposer3 := newGetHeaderPath(1, hash, *proposer3)
		rr = backend.request(t, http.MethodGet, pathProposer3, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, 1, backend.relays[0].GetRequestCount(pathProposer3))
		require.Equal(t, 1, backend.relays[1].GetRequestCount(pathProposer3))
	})
}

func TestGetPayload(t *testing.T) {
	getPayloadPath := "/eth/v1/builder/blinded_blocks"

	parentHash := testutils.HexToHashP("0xe28385e7bd68df656cd0042b74b69c3104b5356ed1f20eb69f1f925df47a3ab7")
	blockHash := testutils.HexToHashP("0x8a5c52e09fcc756bd6d309ce104c9afd1124295547817af915f870b0b4dd2dfd")

	_sk, _ := newKeyPair(t)
	payload := newPayload(t, _sk, 1, parentHash, blockHash)

	t.Run("Okay response from relay", func(t *testing.T) {
		t.Skip()
		backend := newTestBackend(t, 1, time.Second)
		rr := backend.request(t, http.MethodPost, getPayloadPath, payload)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, 1, backend.relays[0].GetRequestCount(getPayloadPath))

		resp := new(types.GetPayloadResponse)
		err := json.Unmarshal(rr.Body.Bytes(), resp)
		require.NoError(t, err)
		require.Equal(t, payload.Message.Body.ExecutionPayloadHeader.BlockHash, resp.Data.BlockHash)
	})

	t.Run("Bad response from relays", func(t *testing.T) {
		t.Skip()
		backend := newTestBackend(t, 2, time.Second)
		resp := new(types.GetPayloadResponse)

		// 1/2 failing responses are okay
		backend.relays[0].GetPayloadResponse = resp
		rr := backend.request(t, http.MethodPost, getPayloadPath, payload)
		require.GreaterOrEqual(t, backend.relays[1].GetRequestCount(getPayloadPath)+backend.relays[0].GetRequestCount(getPayloadPath), 1)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

		// 2/2 failing responses are okay
		backend = newTestBackend(t, 2, time.Second)
		backend.relays[0].GetPayloadResponse = resp
		backend.relays[1].GetPayloadResponse = resp
		rr = backend.request(t, http.MethodPost, getPayloadPath, payload)
		require.Equal(t, 1, backend.relays[0].GetRequestCount(getPayloadPath))
		require.Equal(t, 1, backend.relays[1].GetRequestCount(getPayloadPath))
		require.Equal(t, `{"code":502,"message":"no successful relay response"}`+"\n", rr.Body.String())
		require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
	})

	t.Run("Request specific relay based on proposer", func(t *testing.T) {
		t.SkipNow()
		// Initiate three proposers.
		skP1, proposer1 := newKeyPair(t)
		skP2, proposer2 := newKeyPair(t)
		skP3, proposer3 := newKeyPair(t)

		backend := newTestBackend(t, 2, time.Second)

		// Bind the first proposer to the first relay only.
		backend.boost.pcs.ProposerConfigurations[*proposer1] = &proposerconfig.ProposerConfig{
			Relays: []common.RelayEntry{
				backend.relays[0].RelayEntry,
			},
		}

		// Same goes for second proposer and second relay only.
		backend.boost.pcs.ProposerConfigurations[*proposer2] = &proposerconfig.ProposerConfig{
			Relays: []common.RelayEntry{
				backend.relays[1].RelayEntry,
			},
		}

		// Simulate prior getHeader which perform registration of proposer public key used in the
		// getPayload handler.
		pathProposer1 := newGetHeaderPath(1, parentHash, *proposer1)
		backend.request(t, http.MethodGet, pathProposer1, nil)
		pathProposer2 := newGetHeaderPath(1, parentHash, *proposer2)
		backend.request(t, http.MethodGet, pathProposer2, nil)
		pathProposer3 := newGetHeaderPath(1, parentHash, *proposer3)
		backend.request(t, http.MethodGet, pathProposer3, nil)

		// Now we can proceed to getPayload requests..
		payload1 := newPayload(t, skP1, 1, parentHash, blockHash)
		payload2 := newPayload(t, skP2, 1, parentHash, blockHash)
		payload3 := newPayload(t, skP3, 1, parentHash, blockHash)

		// getPayload from proposer 1 should go only to relay index 0
		rr := backend.request(t, http.MethodPost, getPayloadPath, payload1)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, 1, backend.relays[0].GetRequestCount(getPayloadPath))

		resp := new(types.GetPayloadResponse)
		err := json.Unmarshal(rr.Body.Bytes(), resp)
		require.NoError(t, err)
		require.Equal(t, payload.Message.Body.ExecutionPayloadHeader.BlockHash, resp.Data.BlockHash)

		// getPayload from proposer 2 should go only to relay index 1
		rr = backend.request(t, http.MethodPost, getPayloadPath, payload2)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, 1, backend.relays[1].GetRequestCount(getPayloadPath))

		resp = new(types.GetPayloadResponse)
		err = json.Unmarshal(rr.Body.Bytes(), resp)
		require.NoError(t, err)
		require.Equal(t, payload.Message.Body.ExecutionPayloadHeader.BlockHash, resp.Data.BlockHash)

		// Proposer 3 should be connected to all relays.
		rr = backend.request(t, http.MethodPost, getPayloadPath, payload3)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, 2, backend.relays[1].GetRequestCount(getPayloadPath))
		require.Equal(t, 2, backend.relays[0].GetRequestCount(getPayloadPath))

		resp = new(types.GetPayloadResponse)
		err = json.Unmarshal(rr.Body.Bytes(), resp)
		require.NoError(t, err)
		require.Equal(t, payload.Message.Body.ExecutionPayloadHeader.BlockHash, resp.Data.BlockHash)
	})
}

func TestCheckRelays(t *testing.T) {
	t.Run("At least one relay is okay", func(t *testing.T) {
		backend := newTestBackend(t, 3, time.Second)
		status := backend.boost.CheckRelays()
		require.Equal(t, true, status)
	})

	t.Run("Every relays are down", func(t *testing.T) {
		backend := newTestBackend(t, 1, time.Second)
		backend.relays[0].Server.Close()

		status := backend.boost.CheckRelays()
		require.Equal(t, false, status)
	})

	t.Run("Should not follow redirects", func(t *testing.T) {
		backend := newTestBackend(t, 1, time.Second)
		redirectAddress := backend.relays[0].Server.URL
		backend.relays[0].Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectAddress, http.StatusTemporaryRedirect)
		}))

		url, err := url.ParseRequestURI(backend.relays[0].Server.URL)
		require.NoError(t, err)
		backend.boost.pcs.DefaultConfiguration.Relays[0].URL = url
		status := backend.boost.CheckRelays()
		require.Equal(t, false, status)
	})
}

func TestEmptyTxRoot(t *testing.T) {
	transactions := types.Transactions{}
	txroot, _ := transactions.HashTreeRoot()
	txRootHex := fmt.Sprintf("0x%x", txroot)
	require.Equal(t, "0x7ffe241ea60187fdb0187bfa22de35d1f9bed7ab061d9401fd47e34a54fbede1", txRootHex)
}
