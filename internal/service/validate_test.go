package service

import "testing"

func TestValidateAddInput(t *testing.T) {
	isClient := func(name string) bool { return name == "acme" }

	cases := []struct {
		name    string
		input   AddTaskInput
		project string
		client  string
		wantErr bool
	}{
		{name: "ok plain", input: AddTaskInput{Text: "buy milk"}},
		{name: "ok project", input: AddTaskInput{Text: "x"}, project: "home"},
		{name: "ok nested project", input: AddTaskInput{Text: "x"}, project: "work/clientA"},
		{name: "ok client", input: AddTaskInput{Text: "x"}, client: "acme"},
		{name: "empty text", input: AddTaskInput{Text: "   "}, wantErr: true},
		{name: "newline in text", input: AddTaskInput{Text: "a\nb"}, wantErr: true},
		{name: "tab in text", input: AddTaskInput{Text: "a\tb"}, wantErr: true},
		{name: "del char in text", input: AddTaskInput{Text: "a\x7fb"}, wantErr: true},
		{name: "bad project charset", input: AddTaskInput{Text: "x"}, project: "bad tag!", wantErr: true},
		{name: "project traversal", input: AddTaskInput{Text: "x"}, project: "../escape", wantErr: true},
		{name: "unknown client", input: AddTaskInput{Text: "x"}, client: "ghost", wantErr: true},
		{name: "mutual exclusion", input: AddTaskInput{Text: "x"}, project: "home", client: "acme", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAddInput(tc.input, tc.project, tc.client, isClient)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateAddInput_NilClientValidator(t *testing.T) {
	// A nil validator rejects any non-empty client name.
	if err := ValidateAddInput(AddTaskInput{Text: "x"}, "", "acme", nil); err == nil {
		t.Fatal("expected nil-validator to reject a client name")
	}
	// ...but allows tasks with no client.
	if err := ValidateAddInput(AddTaskInput{Text: "x"}, "home", "", nil); err != nil {
		t.Fatalf("unexpected error with no client: %v", err)
	}
}
