// SPDX-License-Identifier: MIT

package ocpp201

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// keeper is a CSMS that keeps the Authorize request it is given and answers
// with a response it goes on changing.
type keeper struct {
	UnimplementedCSMSHandler
	req  *AuthorizeRequest
	resp *AuthorizeResponse
}

func (k *keeper) Authorize(_ context.Context, _ ocpp.Peer, req *AuthorizeRequest) (*AuthorizeResponse, error) {
	k.req = req
	k.resp = &AuthorizeResponse{IDTokenInfo: IDTokenInfoType{Status: AuthorizationStatusAccepted, EVSEID: []int{1, 2, 3}}}
	return k.resp, nil
}

// A message belongs to whoever holds it. The request a handler receives
// shares no memory with the payload it was decoded from, its slices and the
// raw JSON of CustomData.Fields included: the handler may keep it while the
// payload's memory is used for something else. The response a handler
// returns is encoded before its dispatch returns: changing it afterwards
// does not change what was encoded.
func TestMessagesAreNotShared(t *testing.T) {
	const in = `{"customData":{"vendorId":"V","extra":{"a":[1,2,3]}},"idToken":{"idToken":"ABCDEF","type":"Central","additionalInfo":[{"additionalIdToken":"X1","type":"t1"}]}}`
	payload := []byte(in)
	k := &keeper{}
	out, err := CSMS(k).Dispatch(context.Background(), &fakePeer{v: ocpp.V201}, "Authorize", json.RawMessage(payload))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"idTokenInfo":{"status":"Accepted","evseId":[1,2,3]}}`
	if string(out) != want {
		t.Fatalf("response %s, want %s", out, want)
	}
	for i := range payload {
		payload[i] = '#' // the memory of the payload is reused
	}
	req := k.req
	if req.IDToken.IDToken != "ABCDEF" || len(req.IDToken.AdditionalInfo) != 1 || req.IDToken.AdditionalInfo[0].AdditionalIDToken != "X1" ||
		req.CustomData == nil || req.CustomData.VendorID != "V" || string(req.CustomData.Fields["extra"]) != `{"a":[1,2,3]}` {
		t.Errorf("the request the handler kept changed with the payload: %+v, customData %+v", req, req.CustomData)
	}
	k.resp.IDTokenInfo.Status = AuthorizationStatusBlocked // the handler reuses its response
	k.resp.IDTokenInfo.EVSEID[0] = 99
	if string(out) != want {
		t.Errorf("the encoded response changed with the handler's: %s, want %s", out, want)
	}
}
