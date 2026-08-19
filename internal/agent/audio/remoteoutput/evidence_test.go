package remoteoutput

import (
	"testing"
	"time"
)

func TestEvidenceStoreGetMissingReturnsFalse(t *testing.T) {
	s := NewEvidenceStore()
	if _, ok := s.Get(Destination{ID: "d1", ConfigRevision: "r1"}, "h1"); ok {
		t.Fatal("Get on an empty store: got ok=true, want false")
	}
}

func TestEvidenceStoreOutOfOrderRecordDoesNotRegress(t *testing.T) {
	s := NewEvidenceStore()
	dest := Destination{ID: "d1", ConfigRevision: "r1"}
	fresh := time.Now()
	stale := fresh.Add(-time.Minute)

	s.Record(ProvisioningRecord{Destination: dest, ContentHash: "h1", State: ProvisioningAcknowledged, ObservedAt: fresh})
	s.Record(ProvisioningRecord{Destination: dest, ContentHash: "h1", State: ProvisioningFailed, ObservedAt: stale})

	got, ok := s.Get(dest, "h1")
	if !ok || got.State != ProvisioningAcknowledged {
		t.Errorf("Get after a stale record arrived late: got %+v, want the fresher Acknowledged record retained", got)
	}
}

func TestEvidenceStoreDistinctContentHashesAreIndependent(t *testing.T) {
	s := NewEvidenceStore()
	dest := Destination{ID: "d1", ConfigRevision: "r1"}
	now := time.Now()

	// Same destination, same runtime filename in spirit, two different
	// content hashes: a replaced asset must never share the older
	// asset's evidence.
	s.Record(ProvisioningRecord{Destination: dest, ContentHash: "hash-old", State: ProvisioningAcknowledged, ObservedAt: now})

	if _, ok := s.Get(dest, "hash-new"); ok {
		t.Fatal("Get(hash-new) before it was ever provisioned: got ok=true, want false")
	}
	old, ok := s.Get(dest, "hash-old")
	if !ok || old.State != ProvisioningAcknowledged {
		t.Errorf("Get(hash-old): got %+v, want its own untouched Acknowledged record", old)
	}
}

func TestEvidenceStoreDestinationConfigRevisionIsPartOfTheKey(t *testing.T) {
	s := NewEvidenceStore()
	now := time.Now()
	v1 := Destination{ID: "d1", ConfigRevision: "v1"}
	v2 := Destination{ID: "d1", ConfigRevision: "v2"}

	s.Record(ProvisioningRecord{Destination: v1, ContentHash: "h1", State: ProvisioningManuallyVerified, ObservedAt: now})

	if _, ok := s.Get(v2, "h1"); ok {
		t.Fatal("Get(v2, h1) after only v1 was verified: got ok=true, want false — a configuration change must not carry old evidence forward")
	}
}
