package certissue

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// seedFolder writes a certificate for names into its folder with notAfter.
func seedFolder(t *testing.T, base string, names []string, notAfter time.Time) {
	t.Helper()
	chain, key := selfSigned(t, names[0], notAfter)
	if err := writeFiles(filepath.Join(base, names[0]), chain, key, Meta{Names: names, NotAfter: notAfter.UTC()}); err != nil {
		t.Fatal(err)
	}
}

// recordOrders stands in for Let's Encrypt: it records every order and fails
// any that holds a name in dead.
func recordOrders(t *testing.T, dead ...string) *[][]string {
	t.Helper()
	var mu sync.Mutex
	var orders [][]string
	saved := issueOrder
	issueOrder = func(_ context.Context, _ string, names []string) (Result, error) {
		mu.Lock()
		orders = append(orders, slices.Clone(names))
		mu.Unlock()
		for _, d := range dead {
			if slices.Contains(names, d) {
				return Result{}, fmt.Errorf("%s does not lead port 80 here", d)
			}
		}
		return Result{Names: names}, nil
	}
	t.Cleanup(func() { issueOrder = saved })
	return &orders
}

func joined(orders [][]string) []string {
	out := make([]string, 0, len(orders))
	for _, o := range orders {
		out = append(out, strings.Join(o, ","))
	}
	return out
}

// A folder that is due may hold a name that no longer points here; asking for
// the good names must be able to drop it rather than carry it into the order.
func TestADueFolderIsOrderedForTheNamesAskedOnly(t *testing.T) {
	base := isolate(t)
	seedFolder(t, base, []string{"tun.example.ir", "old.example.ir"}, time.Now().Add(10*24*time.Hour))
	orders := recordOrders(t)
	if _, err := Issue(context.Background(), []string{"tun.example.ir"}); err != nil {
		t.Fatal(err)
	}
	if got := joined(*orders); !slices.Equal(got, []string{"tun.example.ir"}) {
		t.Errorf("ordered %v; a due folder's names must not be carried along", got)
	}
}

// Even a healthy folder can hold a name that stopped pointing here; when the
// widened order fails, the names asked for are ordered alone.
func TestAFailedWidenedOrderFallsBackToTheNamesAsked(t *testing.T) {
	base := isolate(t)
	seedFolder(t, base, []string{"tun.example.ir", "old.example.ir"}, time.Now().Add(60*24*time.Hour))
	orders := recordOrders(t, "old.example.ir")
	if _, err := Issue(context.Background(), []string{"tun.example.ir", "panel.example.ir"}); err != nil {
		t.Fatalf("the names asked for were refused because of one the folder held: %v", err)
	}
	want := []string{"tun.example.ir,panel.example.ir,old.example.ir", "tun.example.ir,panel.example.ir"}
	if got := joined(*orders); !slices.Equal(got, want) {
		t.Errorf("ordered %v, want %v", got, want)
	}
}

// When the names asked for themselves fail, there is nothing to fall back to
// and the operator hears about the failure.
func TestAFailureOfTheNamesAskedIsReported(t *testing.T) {
	base := isolate(t)
	seedFolder(t, base, []string{"tun.example.ir", "vpn.example.ir"}, time.Now().Add(60*24*time.Hour))
	recordOrders(t, "panel.example.ir")
	if _, err := Issue(context.Background(), []string{"tun.example.ir", "panel.example.ir"}); err == nil {
		t.Fatal("an order whose own names failed was reported as done")
	}
}

// Widening never takes an order past Let's Encrypt's hundred names.
func TestWideningStaysWithinTheNameLimit(t *testing.T) {
	base := isolate(t)
	held := []string{"tun.example.ir"}
	for i := range 60 {
		held = append(held, fmt.Sprintf("h%d.example.ir", i))
	}
	seedFolder(t, base, held, time.Now().Add(60*24*time.Hour))
	asked := []string{"tun.example.ir"}
	for i := range 60 {
		asked = append(asked, fmt.Sprintf("a%d.example.ir", i))
	}
	orders := recordOrders(t)
	if _, err := Issue(context.Background(), asked); err != nil {
		t.Fatal(err)
	}
	for _, o := range *orders {
		if len(o) > maxNames {
			t.Errorf("an order of %d names was sent", len(o))
		}
	}
}

// The renewal reads a folder's names under the lock: an issue that widened the
// folder while the renewal waited must not be undone by an order for the old ones.
func TestRenewalDoesNotUndoAnIssueItWaitedFor(t *testing.T) {
	base := isolate(t)
	seedFolder(t, base, []string{"tun.example.ir", "vpn.example.ir"}, time.Now().Add(10*24*time.Hour))
	orders := recordOrders(t)

	issueMu.Lock()
	done := make(chan []error)
	go func() { done <- RenewDue(context.Background(), time.Now()) }()
	time.Sleep(200 * time.Millisecond)
	// What an issue holding the lock would have left: a fresh, wider folder.
	seedFolder(t, base, []string{"tun.example.ir", "panel.example.ir", "vpn.example.ir"}, time.Now().Add(89*24*time.Hour))
	issueMu.Unlock()

	if errs := <-done; len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(*orders) != 0 {
		t.Errorf("the renewal ordered %v over a certificate issued while it waited", joined(*orders))
	}
}
