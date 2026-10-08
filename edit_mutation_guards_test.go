package main

import (
	"context"
	"strings"
	"testing"
)

func TestActiveEditGuardsConversationMutations(t *testing.T) {
	config := editTestHome(t)
	library, err := createLibrary(config.Storage, "Editor checks")
	if err != nil {
		t.Fatal(err)
	}
	project, err := createProject(config.Storage, library.ID, "Reframe")
	if err != nil {
		t.Fatal(err)
	}
	writeEditParentFixture(t, config, "conv_guard", "Original", project.ID)
	app := NewApp()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	app.editOps["op_guard"] = &editOpRun{conversationID: "conv_guard", cancel: cancel}
	for _, test := range []struct {
		name string
		run  func() error
	}{
		{"delete", func() error { return app.DeleteConversation("conv_guard") }},
		{"rename", func() error { _, err := app.UpdateConversationTitle("conv_guard", "Changed"); return err }},
		{"move", func() error { _, err := app.MoveConversationToProject("conv_guard", ""); return err }},
		{"override", func() error {
			_, err := app.SetConversationModelOverrides("conv_guard", ConversationModelOverrides{})
			return err
		}},
		{"project-delete", func() error { _, err := app.DeleteProject(project.ID); return err }},
		{"library-delete", func() error { _, err := app.DeleteLibrary(library.ID); return err }},
		{"purge", func() error { _, err := app.PurgeArchivedConversations(); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err == nil || !strings.Contains(err.Error(), "running") {
				t.Fatalf("expected active-job refusal, got %v", err)
			}
			if _, err := getConversation(config.Storage, "conv_guard"); err != nil {
				t.Fatalf("running source changed: %v", err)
			}
		})
	}
	delete(app.editOps, "op_guard")
	if _, err := app.UpdateConversationTitle("conv_guard", "Completed"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.MoveConversationToProject("conv_guard", ""); err != nil {
		t.Fatal(err)
	}
	if err := app.DeleteConversation("conv_guard"); err != nil {
		t.Fatal(err)
	}
}

func TestActiveEditAllowsUnrelatedParentDeletion(t *testing.T) {
	config := editTestHome(t)
	writeEditParentFixture(t, config, "conv_parent_guard", "Parent", "")
	app := NewApp()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	app.editOps["op_child"] = &editOpRun{conversationID: "conv_child_guard", cancel: cancel}
	if err := app.DeleteConversation("conv_parent_guard"); err != nil {
		t.Fatalf("independent child's render blocked parent deletion: %v", err)
	}
}

func TestEditorShutdownCancelsLocalJobs(t *testing.T) {
	app := NewApp()
	ctx, cancel := context.WithCancel(context.Background())
	app.editOps["op_shutdown"] = &editOpRun{conversationID: "conv_shutdown", cancel: cancel}
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		app.editOpsMu.Lock()
		delete(app.editOps, "op_shutdown")
		app.editOpsMu.Unlock()
		close(done)
	}()
	app.shutdown(context.Background())
	<-done
	if ctx.Err() == nil || app.anyEditOperationActive() {
		t.Fatal("shutdown did not cancel/reap edit jobs")
	}
}
