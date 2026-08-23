package connections

import "testing"

func TestAddGetListRemoveRoundtrip(t *testing.T) {
	t.Setenv("GODL_DATA_DIR", t.TempDir())

	if _, err := Get("mynas"); err == nil {
		t.Fatal("Get of a connection that doesn't exist should error")
	}

	if err := Add(Connection{Name: "mynas", Type: TypeWebDAV, URL: "https://dav.example.com/", Username: "alice", Password: "secret"}); err != nil {
		t.Fatal(err)
	}

	got, err := Get("mynas")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://dav.example.com/" || got.Username != "alice" || got.Password != "secret" {
		t.Errorf("Get returned %+v, want the saved fields back", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt was not set on Add")
	}

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("List() = %d connections, want 1", len(list))
	}

	// Adding again with the same name overwrites in place rather than
	// appending a second entry, and keeps the original CreatedAt.
	if err := Add(Connection{Name: "mynas", Type: TypeWebDAV, URL: "https://dav.example.com/new/", Username: "bob"}); err != nil {
		t.Fatal(err)
	}
	list, err = List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("List() after overwrite = %d connections, want 1", len(list))
	}
	if list[0].URL != "https://dav.example.com/new/" || list[0].Username != "bob" {
		t.Errorf("overwrite didn't take effect: %+v", list[0])
	}
	if list[0].CreatedAt != got.CreatedAt {
		t.Errorf("overwrite changed CreatedAt: got %v, want %v", list[0].CreatedAt, got.CreatedAt)
	}

	if err := Remove("mynas"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("mynas"); err == nil {
		t.Fatal("Get after Remove should error")
	}
	if err := Remove("mynas"); err == nil {
		t.Fatal("Remove of an already-removed connection should error")
	}
}
