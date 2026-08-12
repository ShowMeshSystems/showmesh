package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestInTxCommitsOnSuccess proves the ordinary path: fn's writes are
// visible after InTx returns nil.
func TestInTxCommitsOnSuccess(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.DeclareNode(ctx, NodeDeclarationRecord{NodeID: "node-a", Label: "A"})
		return err
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	rec, err := st.GetNodeDeclaration(ctx, "node-a")
	if err != nil {
		t.Fatalf("get node declaration after commit: %v", err)
	}
	if rec.Label != "A" {
		t.Errorf("Label = %q, want %q", rec.Label, "A")
	}
}

// TestInTxRollsBackOnError proves the other half: a write inside a closure
// that goes on to return an error is NOT visible afterward — the whole
// point of InTx existing (ADR-024 decision 11's same-transaction rule).
// Broken deliberately (by making fn return an error after a real write) to
// confirm this test would fail if InTx silently committed anyway.
func TestInTxRollsBackOnError(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	sentinel := errors.New("boom")
	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.DeclareNode(ctx, NodeDeclarationRecord{NodeID: "node-b", Label: "B"}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx error = %v, want it to wrap the sentinel", err)
	}

	if _, err := st.GetNodeDeclaration(ctx, "node-b"); !errors.Is(err, ErrNodeDeclarationNotFound) {
		t.Errorf("node declaration exists after its transaction returned an error: err = %v, want ErrNodeDeclarationNotFound", err)
	}
}

// TestInTxRollsBackOnPanic proves the panic half of InTx's contract: a
// panic inside fn must not leave a partial write committed, and InTx must
// re-panic rather than swallow it (recovered here only so the test itself
// can assert on the aftermath).
func TestInTxRollsBackOnPanic(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("InTx did not re-panic")
			}
		}()
		_ = st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
			if _, err := tx.DeclareNode(ctx, NodeDeclarationRecord{NodeID: "node-c", Label: "C"}); err != nil {
				t.Fatalf("declare node: %v", err)
			}
			panic("deliberate panic inside InTx closure")
		})
	}()

	if _, err := st.GetNodeDeclaration(ctx, "node-c"); !errors.Is(err, ErrNodeDeclarationNotFound) {
		t.Errorf("node declaration exists after its transaction's closure panicked: err = %v, want ErrNodeDeclarationNotFound", err)
	}
}

// TestStoreMethodCalledInsideInTxPanics is acceptance criterion 3: a
// *Store method called from inside an InTx closure must panic naming the
// method, rather than hanging forever on the single-connection pool (see
// guardNotInTx's doc comment in tx.go). Broken deliberately first — with
// the guard call removed from a method, this exact test would hang instead
// of panicking, which is why guardNotInTx must be a hard panic rather than
// a value a caller could accidentally ignore.
func TestStoreMethodCalledInsideInTxPanics(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	var panicMsg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicMsg, _ = r.(string)
			}
		}()
		_ = st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
			// Calling the *Store form (not the *Tx form) from inside this
			// closure is exactly the programming error guardNotInTx exists
			// to catch — ctx here carries InTx's marker.
			_, err := st.ListNodeDeclarations(ctx)
			return err
		})
	}()

	if panicMsg == "" {
		t.Fatalf("Store.ListNodeDeclarations called from inside InTx did not panic")
	}
	if !strings.Contains(panicMsg, "Store.ListNodeDeclarations") {
		t.Errorf("panic message = %q, want it to name Store.ListNodeDeclarations", panicMsg)
	}
	if !strings.Contains(panicMsg, "Tx") {
		t.Errorf("panic message = %q, want it to point at the (*Tx) method to use instead", panicMsg)
	}
}

// TestNestedInTxPanicsRatherThanHanging is F1's HIGH finding, reproduced
// directly: InTx itself used to be the one database entry point with no
// guardNotInTx call, so a caller nesting a second InTx inside an
// already-open one (the exact shape identity.Service.AuditedWrite's own
// InTx call takes when something inside its fn calls another
// Service method that itself opens a transaction) blocked forever on the
// single-connection pool — confirmed by running this exact scenario before
// the fix: a 4-second test timeout fired with no panic, no error, and no
// log line. With guardNotInTx now called at the top of InTx, the same
// scenario panics immediately instead. A context.WithTimeout bounds this
// test so a regression back to the hanging behavior fails loudly and
// quickly (not by exhausting `go test`'s own default 30s-per-package
// timeout with no attribution) rather than needing to be diagnosed the way
// the original defect was.
func TestNestedInTxPanicsRatherThanHanging(t *testing.T) {
	st := openTestStore(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	var panicMsg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicMsg, _ = r.(string)
			}
		}()
		_ = st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
			// The nested call: InTx called AGAIN with a context already
			// carrying the outer InTx's marker. This must panic rather than
			// block on the connection the outer transaction already holds.
			return st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
				return nil
			})
		})
	}()

	if ctx.Err() != nil {
		t.Fatalf("nested InTx call hung until the test's own timeout instead of panicking (ctx.Err() = %v) — this is exactly the coordinator-wedging defect this test exists to catch", ctx.Err())
	}
	if panicMsg == "" {
		t.Fatalf("nested Store.InTx call did not panic")
	}
	if !strings.Contains(panicMsg, "Store.InTx") {
		t.Errorf("panic message = %q, want it to name Store.InTx", panicMsg)
	}
}

// TestGuardNotInTxAllowsOrdinaryContext proves guardNotInTx does NOT fire
// for a context that never went through InTx — the common case every
// other test in this package's suite already exercises implicitly, made
// explicit here so a future change to the marker's zero-value handling
// cannot silently start panicking on every ordinary call.
func TestGuardNotInTxAllowsOrdinaryContext(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("guardNotInTx panicked on an ordinary (non-InTx) context: %v", r)
		}
	}()
	guardNotInTx(context.Background(), "Store.Whatever")
}
