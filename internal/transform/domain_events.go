package transform

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"golang.org/x/crypto/sha3"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
)

// DomainEventsFromContractEvents decodes Soroban Domains registry events out of
// already-extracted contract events. Events from other contracts are ignored.
func DomainEventsFromContractEvents(events []store.ContractEvent, registryIDs []string) []store.DomainEvent {
	set := registryIDSet(registryIDs)
	if len(events) == 0 || len(set) == 0 {
		return nil
	}

	out := make([]store.DomainEvent, 0)
	for _, ce := range events {
		if _, ok := set[ce.ContractID]; !ok {
			continue
		}
		ev, err := decodeDomainEvent(ce)
		if err != nil {
			log.Printf("domain_events: skip registry event contract=%s tx=%s topics=%q/%q/%q: %v",
				ce.ContractID, ce.TransactionHash, deref(ce.Topic1), deref(ce.Topic2), deref(ce.Topic3), err)
			continue
		}
		if ev != nil {
			out = append(out, *ev)
		}
	}
	return out
}

func registryIDSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

func decodeDomainEvent(ce store.ContractEvent) (*store.DomainEvent, error) {
	topic1 := deref(ce.Topic1)
	topic2 := deref(ce.Topic2)

	var data xdr.ScVal
	if ce.ValueXDR != "" {
		raw, err := base64.StdEncoding.DecodeString(ce.ValueXDR)
		if err != nil {
			return nil, fmt.Errorf("decode value xdr: %w", err)
		}
		if err := xdr.SafeUnmarshal(raw, &data); err != nil {
			return nil, fmt.Errorf("unmarshal value: %w", err)
		}
	}

	fields := scValFields(data)
	ev := store.DomainEvent{
		TransactionHash: ce.TransactionHash,
		LedgerSequence:  ce.LedgerSequence,
		CreatedAt:       ce.CreatedAt,
	}

	switch {
	case topic1 == "REGISTRY" && topic2 == "DOMAIN":
		ev.EventType = store.DomainEventRegister
		ev.Owner = fieldAddress(fields, "register", 0)
		ev.Label = fieldBytesOrString(fields, "domain", 1)
		ev.TLD = fieldBytesOrString(fields, "tld", 2)
		ev.ResolvedAddress = fieldAddress(fields, "address", 3)
		if exp := fieldU64(fields, "exp_date", 4); exp > 0 {
			t := time.Unix(int64(exp), 0).UTC()
			ev.ExpiresAt = &t
		}
		if ev.Label != "" && ev.TLD != "" {
			ev.Name = ev.Label + "." + ev.TLD
			ev.Node = generateDomainNode(ev.Label, ev.TLD)
		}
		regAt := ce.CreatedAt
		ev.RegisteredAt = &regAt
	case topic1 == "UPDATE" && topic2 == "RECORD":
		ev.EventType = store.DomainEventTransfer
		ev.Node = nodeFromTopic(ce.Topic3)
		ev.ResolvedAddress = fieldAddress(fields, "to", 1)
	case topic1 == "RENEW" && topic2 == "DOMAIN":
		ev.EventType = store.DomainEventRenew
		ev.Node = nodeFromTopic(ce.Topic3)
		if exp := fieldU64(fields, "exp_date", 2); exp > 0 {
			t := time.Unix(int64(exp), 0).UTC()
			ev.ExpiresAt = &t
		}
	case topic1 == "CLAIM" && topic2 == "DOMAIN":
		ev.EventType = store.DomainEventClaim
		ev.Node = nodeFromTopic(ce.Topic3)
		ev.Owner = fieldAddress(fields, "register", 0)
		ev.ResolvedAddress = fieldAddress(fields, "address", 1)
		if exp := fieldU64(fields, "exp_date", 2); exp > 0 {
			t := time.Unix(int64(exp), 0).UTC()
			ev.ExpiresAt = &t
		}
	case topic1 == "EVICT" && topic2 == "DOMAIN":
		ev.EventType = store.DomainEventRevoke
		ev.Node = nodeFromTopic(ce.Topic3)
	default:
		return nil, nil
	}

	if ev.Node == "" {
		return nil, fmt.Errorf("domain event missing node")
	}

	details, _ := json.Marshal(map[string]string{
		"event_type": ev.EventType,
		"topic_1":    topic1,
		"topic_2":    topic2,
	})
	ev.Details = string(details)
	return &ev, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nodeFromTopic(topic *string) string {
	s := deref(topic)
	s = strings.TrimPrefix(s, "0x")
	return strings.ToLower(s)
}

type scFields struct {
	byName map[string]xdr.ScVal
	vec    []xdr.ScVal
}

func scValFields(v xdr.ScVal) scFields {
	f := scFields{byName: map[string]xdr.ScVal{}}
	switch v.Type {
	case xdr.ScValTypeScvMap:
		if v.Map != nil && *v.Map != nil {
			for _, e := range **v.Map {
				f.byName[scValToString(e.Key)] = e.Val
			}
		}
	case xdr.ScValTypeScvVec:
		if v.Vec != nil && *v.Vec != nil {
			f.vec = **v.Vec
		}
	}
	return f
}

func fieldVal(f scFields, name string, idx int) (xdr.ScVal, bool) {
	if v, ok := f.byName[name]; ok {
		return v, true
	}
	if idx >= 0 && idx < len(f.vec) {
		return f.vec[idx], true
	}
	return xdr.ScVal{}, false
}

func fieldAddress(f scFields, name string, idx int) string {
	v, ok := fieldVal(f, name, idx)
	if !ok {
		return ""
	}
	if v.Type == xdr.ScValTypeScvAddress && v.Address != nil {
		return scAddressToString(*v.Address)
	}
	s := scValToString(v)
	if strings.HasPrefix(s, "G") || strings.HasPrefix(s, "C") {
		return s
	}
	return ""
}

func fieldBytesOrString(f scFields, name string, idx int) string {
	v, ok := fieldVal(f, name, idx)
	if !ok {
		return ""
	}
	switch v.Type {
	case xdr.ScValTypeScvString:
		if v.Str != nil {
			return string(*v.Str)
		}
	case xdr.ScValTypeScvBytes:
		if v.Bytes != nil {
			return string([]byte(*v.Bytes))
		}
	case xdr.ScValTypeScvSymbol:
		if v.Sym != nil {
			return string(*v.Sym)
		}
	}
	return scValToString(v)
}

func fieldU64(f scFields, name string, idx int) uint64 {
	v, ok := fieldVal(f, name, idx)
	if !ok {
		return 0
	}
	switch v.Type {
	case xdr.ScValTypeScvU64:
		if v.U64 != nil {
			return uint64(*v.U64)
		}
	case xdr.ScValTypeScvI64:
		if v.I64 != nil {
			return uint64(*v.I64)
		}
	}
	return 0
}

func keccak256(data []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write(data)
	return h.Sum(nil)
}

// generateDomainNode hashes label.tld the way the Soroban Domains registry
// does: keccak256(keccak256(tld) || keccak256(label)).
func generateDomainNode(label, tld string) string {
	tldHash := keccak256([]byte(tld))
	labelHash := keccak256([]byte(label))
	joined := make([]byte, 0, len(tldHash)+len(labelHash))
	joined = append(joined, tldHash...)
	joined = append(joined, labelHash...)
	return hex.EncodeToString(keccak256(joined))
}

func contractIDFromStrkey(id string) (*xdr.ContractId, error) {
	raw, err := strkey.Decode(strkey.VersionByteContract, id)
	if err != nil {
		return nil, err
	}
	var cid xdr.ContractId
	copy(cid[:], raw)
	return &cid, nil
}
