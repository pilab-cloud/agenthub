package registry

import (
	"errors"
	"testing"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
)

func TestMemStoreCreateGetList(t *testing.T) {
	s := NewMemStore()
	ns, err := s.Create(&commonv1.Namespace{Slug: "pilab", DisplayName: "PiLab"})
	if err != nil {
		t.Fatal(err)
	}
	if ns.Slug != "pilab" {
		t.Fatalf("slug: %s", ns.Slug)
	}
	if ns.Visibility != commonv1.NamespaceVisibility_NAMESPACE_VISIBILITY_PUBLIC {
		t.Fatalf("default visibility not public")
	}
	got, ok := s.Get("pilab")
	if !ok || got.DisplayName != "PiLab" {
		t.Fatalf("get failed")
	}
	if _, ok := s.Get("nope"); ok {
		t.Fatal("expected not found")
	}
	list := s.List()
	if len(list) != 1 {
		t.Fatalf("list len: %d", len(list))
	}
}

func TestMemStoreInvalidSlug(t *testing.T) {
	s := NewMemStore()
	if _, err := s.Create(&commonv1.Namespace{Slug: "Bad"}); !errors.Is(err, ErrInvalidSlug) {
		t.Fatalf("want ErrInvalidSlug, got %v", err)
	}
	if _, err := s.Create(&commonv1.Namespace{Slug: "a"}); !errors.Is(err, ErrInvalidSlug) {
		t.Fatalf("single char should be invalid per spec")
	}
}

func TestMemStoreDuplicate(t *testing.T) {
	s := NewMemStore()
	if _, err := s.Create(&commonv1.Namespace{Slug: "dup"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(&commonv1.Namespace{Slug: "dup"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
}
