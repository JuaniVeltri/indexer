package transform

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/config"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
)

const (
	testOwner    = "GBKPF4URAGUGPBFKQNMDDD4IY5BRRXRK2VEBULJEMVULCCODND436NIO"
	testResolved = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
	testTxHash   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testNodeHex  = "2fe4cc6a15f9466bad71ed407a8f1b7da81efd931e7712753152aa17abc0e06e"
)

func TestGenerateDomainNode_MatchesSDK(t *testing.T) {
	// Vectors from @creit-tech/sorobandomains-sdk generateNode(["stellar"], "xlm")
	got := generateDomainNode("stellar", "xlm")
	if got != testNodeHex {
		t.Errorf("generateDomainNode(stellar, xlm) = %s, want %s", got, testNodeHex)
	}

	xlm := keccak256([]byte("xlm"))
	wantXLM := "745bb28999b7d22e04ecc9719a460ca08b4c0ac2044adf23ad2bb4db8f8eaf6b"
	if got := hex.EncodeToString(xlm); got != wantXLM {
		t.Errorf("keccak256(xlm) = %s, want %s", got, wantXLM)
	}
}

func TestDomainEventsFromContractEvents_EachType(t *testing.T) {
	registry := config.DefaultPublicDomainsRegistry
	exp := time.Unix(1800000000, 0).UTC()
	created := time.Unix(1700000000, 0).UTC()

	cases := []struct {
		name      string
		build     func() store.ContractEvent
		wantType  string
		wantName  string
		wantNode  string
		wantOwner string
		wantAddr  string
		wantExp   bool
	}{
		{
			name: "register",
			build: func() store.ContractEvent {
				return mustDomainContractEvent(t, registry, []xdr.ScVal{scSymbol("REGISTRY"), scSymbol("DOMAIN")}, scMap(
					"register", scAccount(t, testOwner),
					"domain", scBytes("stellar"),
					"tld", scBytes("xlm"),
					"address", scAccount(t, testResolved),
					"exp_date", scU64(uint64(exp.Unix())),
					"amount_paid", scU128(100),
				), created)
			},
			wantType:  store.DomainEventRegister,
			wantName:  "stellar.xlm",
			wantNode:  testNodeHex,
			wantOwner: testOwner,
			wantAddr:  testResolved,
			wantExp:   true,
		},
		{
			name: "transfer",
			build: func() store.ContractEvent {
				return mustDomainContractEvent(t, registry, []xdr.ScVal{
					scSymbol("UPDATE"), scSymbol("RECORD"), scBytesRaw(mustNodeBytes(t)),
				}, scMap(
					"from", scAccount(t, testOwner),
					"to", scAccount(t, testResolved),
				), created)
			},
			wantType: store.DomainEventTransfer,
			wantNode: testNodeHex,
			wantAddr: testResolved,
		},
		{
			name: "renew",
			build: func() store.ContractEvent {
				return mustDomainContractEvent(t, registry, []xdr.ScVal{
					scSymbol("RENEW"), scSymbol("DOMAIN"), scBytesRaw(mustNodeBytes(t)),
				}, scMap(
					"payer", scAccount(t, testOwner),
					"amount_paid", scU128(50),
					"exp_date", scU64(uint64(exp.Unix())),
				), created)
			},
			wantType: store.DomainEventRenew,
			wantNode: testNodeHex,
			wantExp:  true,
		},
		{
			name: "revoke",
			build: func() store.ContractEvent {
				return mustDomainContractEvent(t, registry, []xdr.ScVal{
					scSymbol("EVICT"), scSymbol("DOMAIN"), scBytesRaw(mustNodeBytes(t)),
				}, xdr.ScVal{Type: xdr.ScValTypeScvVoid}, created)
			},
			wantType: store.DomainEventRevoke,
			wantNode: testNodeHex,
		},
		{
			name: "claim",
			build: func() store.ContractEvent {
				return mustDomainContractEvent(t, registry, []xdr.ScVal{
					scSymbol("CLAIM"), scSymbol("DOMAIN"), scBytesRaw(mustNodeBytes(t)),
				}, scMap(
					"register", scAccount(t, testResolved),
					"address", scAccount(t, testResolved),
					"exp_date", scU64(uint64(exp.Unix())),
					"amount_paid", scU128(75),
				), created)
			},
			wantType:  store.DomainEventClaim,
			wantNode:  testNodeHex,
			wantOwner: testResolved,
			wantAddr:  testResolved,
			wantExp:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ce := tc.build()

			got := DomainEventsFromContractEvents([]store.ContractEvent{ce}, []string{registry})
			if len(got) != 1 {
				t.Fatalf("got %d events, want 1", len(got))
			}
			ev := got[0]
			if ev.EventType != tc.wantType {
				t.Errorf("EventType = %q, want %q", ev.EventType, tc.wantType)
			}
			if tc.wantName != "" && ev.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", ev.Name, tc.wantName)
			}
			if ev.Node != tc.wantNode {
				t.Errorf("Node = %q, want %q", ev.Node, tc.wantNode)
			}
			if tc.wantOwner != "" && ev.Owner != tc.wantOwner {
				t.Errorf("Owner = %q, want %q", ev.Owner, tc.wantOwner)
			}
			if tc.wantAddr != "" && ev.ResolvedAddress != tc.wantAddr {
				t.Errorf("ResolvedAddress = %q, want %q", ev.ResolvedAddress, tc.wantAddr)
			}
			if tc.wantExp {
				if ev.ExpiresAt == nil || !ev.ExpiresAt.Equal(exp) {
					t.Errorf("ExpiresAt = %v, want %v", ev.ExpiresAt, exp)
				}
			}
		})
	}
}

func TestDomainEventsFromRecordedFixtures(t *testing.T) {
	registry := config.DefaultPublicDomainsRegistry
	created := time.Unix(1700000000, 0).UTC()
	cases := []struct {
		file     string
		wantType string
	}{
		{"register.json", store.DomainEventRegister},
		{"transfer.json", store.DomainEventTransfer},
		{"renew.json", store.DomainEventRenew},
		{"revoke.json", store.DomainEventRevoke},
		{"claim.json", store.DomainEventClaim},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "domains", tc.file))
			if err != nil {
				t.Fatalf("recorded fixture missing %s (run TestDomainEventsFromContractEvents_EachType): %v", tc.file, err)
			}
			var fx struct {
				Topic1   string `json:"topic_1"`
				Topic2   string `json:"topic_2"`
				Topic3   string `json:"topic_3"`
				ValueXDR string `json:"value_xdr"`
			}
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatal(err)
			}
			t1, t2, t3 := fx.Topic1, fx.Topic2, fx.Topic3
			ce := store.ContractEvent{
				ContractID:      registry,
				Topic1:          &t1,
				Topic2:          &t2,
				Topic3:          &t3,
				ValueXDR:        fx.ValueXDR,
				TransactionHash: testTxHash,
				CreatedAt:       created,
			}
			got := DomainEventsFromContractEvents([]store.ContractEvent{ce}, []string{registry})
			if len(got) != 1 {
				t.Fatalf("got %d events, want 1", len(got))
			}
			if got[0].EventType != tc.wantType {
				t.Errorf("EventType = %q, want %q", got[0].EventType, tc.wantType)
			}
		})
	}
}

func TestDomainEventsFromContractEvents_IgnoresOtherContracts(t *testing.T) {
	other, err := contractIDFromStrkey("CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4")
	if err != nil {
		t.Fatalf("other contract id: %v", err)
	}
	_ = other
	ce := mustDomainContractEvent(t, "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4",
		[]xdr.ScVal{scSymbol("REGISTRY"), scSymbol("DOMAIN")},
		scMap("domain", scBytes("stellar"), "tld", scBytes("xlm")),
		time.Unix(1700000000, 0).UTC())

	got := DomainEventsFromContractEvents([]store.ContractEvent{ce}, []string{config.DefaultPublicDomainsRegistry})
	if len(got) != 0 {
		t.Errorf("expected no events from a non-registry contract, got %d", len(got))
	}
}

func TestDomainEventsFromContractEvents_EmptyRegistryIDs(t *testing.T) {
	ce := mustDomainContractEvent(t, config.DefaultPublicDomainsRegistry,
		[]xdr.ScVal{scSymbol("REGISTRY"), scSymbol("DOMAIN")},
		scMap("domain", scBytes("stellar"), "tld", scBytes("xlm")),
		time.Unix(1700000000, 0).UTC())
	got := DomainEventsFromContractEvents([]store.ContractEvent{ce}, nil)
	if got != nil {
		t.Errorf("expected nil when no registry IDs configured, got %v", got)
	}
}

func TestDomainEventsFromContractEvents_DecodeErrorSkipped(t *testing.T) {
	t1, t2 := "REGISTRY", "DOMAIN"
	ce := store.ContractEvent{
		ContractID:      config.DefaultPublicDomainsRegistry,
		Topic1:          &t1,
		Topic2:          &t2,
		ValueXDR:        "!!!not-xdr!!!",
		TransactionHash: testTxHash,
	}
	got := DomainEventsFromContractEvents([]store.ContractEvent{ce}, []string{config.DefaultPublicDomainsRegistry})
	if len(got) != 0 {
		t.Errorf("decode error should skip the event, got %d", len(got))
	}
}

func TestDomainEventsFromTransaction_RecordedXDR(t *testing.T) {
	created := time.Unix(1700000000, 0).UTC()
	exp := time.Unix(1800000000, 0).UTC()

	cases := []struct {
		file     string
		wantType string
		wantName string
		topics   []xdr.ScVal
		data     xdr.ScVal
	}{
		{
			file:     "register_meta.xdr",
			wantType: store.DomainEventRegister,
			wantName: "stellar.xlm",
			topics:   []xdr.ScVal{scSymbol("REGISTRY"), scSymbol("DOMAIN")},
			data: scMap(
				"register", scAccount(t, testOwner),
				"domain", scBytes("stellar"),
				"tld", scBytes("xlm"),
				"address", scAccount(t, testResolved),
				"exp_date", scU64(uint64(exp.Unix())),
				"amount_paid", scU128(100),
			),
		},
		{
			file:     "transfer_meta.xdr",
			wantType: store.DomainEventTransfer,
			topics: []xdr.ScVal{
				scSymbol("UPDATE"), scSymbol("RECORD"), scBytesRaw(mustNodeBytes(t)),
			},
			data: scMap(
				"from", scAccount(t, testOwner),
				"to", scAccount(t, testResolved),
			),
		},
		{
			file:     "renew_meta.xdr",
			wantType: store.DomainEventRenew,
			topics: []xdr.ScVal{
				scSymbol("RENEW"), scSymbol("DOMAIN"), scBytesRaw(mustNodeBytes(t)),
			},
			data: scMap(
				"payer", scAccount(t, testOwner),
				"amount_paid", scU128(50),
				"exp_date", scU64(uint64(exp.Unix())),
			),
		},
		{
			file:     "claim_meta.xdr",
			wantType: store.DomainEventClaim,
			topics: []xdr.ScVal{
				scSymbol("CLAIM"), scSymbol("DOMAIN"), scBytesRaw(mustNodeBytes(t)),
			},
			data: scMap(
				"register", scAccount(t, testResolved),
				"address", scAccount(t, testResolved),
				"exp_date", scU64(uint64(exp.Unix())),
				"amount_paid", scU128(75),
			),
		},
		{
			file:     "revoke_meta.xdr",
			wantType: store.DomainEventRevoke,
			topics: []xdr.ScVal{
				scSymbol("EVICT"), scSymbol("DOMAIN"), scBytesRaw(mustNodeBytes(t)),
			},
			data: xdr.ScVal{Type: xdr.ScValTypeScvVoid},
		},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			wantMeta := mustDomainMetaXDR(t, tc.topics, tc.data)
			path := filepath.Join("testdata", "domains", tc.file)
			if os.Getenv("WRITE_DOMAIN_FIXTURES") == "1" {
				if err := os.WriteFile(path, []byte(wantMeta+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			recorded, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("recorded TransactionMeta fixture missing %s: %v", tc.file, err)
			}
			if got := string(bytes.TrimSpace(recorded)); got != wantMeta {
				t.Errorf("fixture %s does not match marshaled TransactionMeta", tc.file)
			}

			entry := loadTransactionsFixture(t)[0]
			entry.ResultMetaXDR = string(bytes.TrimSpace(recorded))
			entry.Ledger = 100
			entry.CreatedAt = created.Unix()

			ces, err := ContractEventsFromTransaction(entry, "Test SDF Network ; September 2015")
			if err != nil {
				t.Fatalf("ContractEventsFromTransaction: %v", err)
			}
			got := DomainEventsFromContractEvents(ces, []string{config.DefaultPublicDomainsRegistry})
			if len(got) != 1 {
				t.Fatalf("got %d domain events, want 1 (contract events=%d)", len(got), len(ces))
			}
			if got[0].EventType != tc.wantType {
				t.Errorf("EventType = %q, want %q", got[0].EventType, tc.wantType)
			}
			if tc.wantName != "" && got[0].Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got[0].Name, tc.wantName)
			}
		})
	}
}

func mustDomainMetaXDR(t *testing.T, topics []xdr.ScVal, data xdr.ScVal) string {
	t.Helper()
	cid, err := contractIDFromStrkey(config.DefaultPublicDomainsRegistry)
	if err != nil {
		t.Fatal(err)
	}
	ce := xdr.ContractEvent{
		ContractId: cid,
		Type:       xdr.ContractEventTypeContract,
		Body: xdr.ContractEventBody{
			V:  0,
			V0: &xdr.ContractEventV0{Topics: topics, Data: data},
		},
	}
	meta := xdr.TransactionMeta{
		V: 3,
		V3: &xdr.TransactionMetaV3{
			SorobanMeta: &xdr.SorobanTransactionMeta{
				Events:      []xdr.ContractEvent{ce},
				ReturnValue: xdr.ScVal{Type: xdr.ScValTypeScvVoid},
			},
		},
	}
	b64, err := xdr.MarshalBase64(meta)
	if err != nil {
		t.Fatal(err)
	}
	return b64
}

func mustDomainContractEvent(t *testing.T, contractID string, topics []xdr.ScVal, data xdr.ScVal, created time.Time) store.ContractEvent {
	t.Helper()
	cid, err := contractIDFromStrkey(contractID)
	if err != nil {
		t.Fatal(err)
	}
	raw := xdr.ContractEvent{
		ContractId: cid,
		Type:       xdr.ContractEventTypeContract,
		Body: xdr.ContractEventBody{
			V:  0,
			V0: &xdr.ContractEventV0{Topics: topics, Data: data},
		},
	}
	ev, err := contractEventFromXDR(raw, testTxHash, 100, created.Unix())
	if err != nil {
		t.Fatal(err)
	}
	return *ev
}

func mustNodeBytes(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(testNodeHex)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func scSymbol(s string) xdr.ScVal {
	sym := xdr.ScSymbol(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}
}

func scBytes(s string) xdr.ScVal {
	b := xdr.ScBytes(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &b}
}

func scBytesRaw(raw []byte) xdr.ScVal {
	b := xdr.ScBytes(raw)
	return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &b}
}

func scU64(n uint64) xdr.ScVal {
	v := xdr.Uint64(n)
	return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &v}
}

func scU128(n uint64) xdr.ScVal {
	parts := xdr.UInt128Parts{Hi: 0, Lo: xdr.Uint64(n)}
	return xdr.ScVal{Type: xdr.ScValTypeScvU128, U128: &parts}
}

func scAccount(t *testing.T, addr string) xdr.ScVal {
	t.Helper()
	id := xdr.MustAddress(addr)
	a := xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &id}
	return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &a}
}

func scMap(pairs ...interface{}) xdr.ScVal {
	if len(pairs)%2 != 0 {
		panic("scMap requires even number of args")
	}
	m := make(xdr.ScMap, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key := scSymbol(pairs[i].(string))
		val := pairs[i+1].(xdr.ScVal)
		m = append(m, xdr.ScMapEntry{Key: key, Val: val})
	}
	mp := &m
	return xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &mp}
}
