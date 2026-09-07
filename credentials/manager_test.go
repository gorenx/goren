package credentials_test

import (
	"context"
	"testing"

	"github.com/gorenx/goren/credentials"
)

type untouchedStore struct{}

func (untouchedStore) Source() string { return "fixture" }
func (untouchedStore) Load(context.Context, string) (string, bool, error) {
	panic("invalid name reached storage")
}
func (untouchedStore) Save(context.Context, string, string) error {
	panic("invalid name reached storage")
}
func (untouchedStore) Delete(context.Context, string) error { panic("invalid name reached storage") }

type emptyEnvironment struct{}

func (emptyEnvironment) Lookup(string) (string, bool) { return "", false }

func TestManagerRejectsInvalidNamesBeforeStorage(t *testing.T) {
	business, err := credentials.NewManager(untouchedStore{}, emptyEnvironment{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "1KEY", "API-KEY", "KEY=VALUE", "KEY\n"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := business.Resolve(ctx, name); err == nil {
				t.Fatal("invalid resolve name accepted")
			}
			if _, err := business.Describe(ctx, name); err == nil {
				t.Fatal("invalid describe name accepted")
			}
			if err := business.Set(ctx, name, "fixture"); err == nil {
				t.Fatal("invalid write name accepted")
			}
			if err := business.Unset(ctx, name); err == nil {
				t.Fatal("invalid removal name accepted")
			}
		})
	}
}
