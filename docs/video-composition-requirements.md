# Video Composition Workspace — Product Requirements

Status: v1 implemented (document + workspace, render-to-preview, export, seeded chat intent); proposals and the in-workspace chat surface are v2  
Date: 20 October 2026 (v1 shipped October 2026)  
Product: Atelier

## 1. Purpose

Enable users to assemble multiple video clips into a finished video, configure transitions, add and mix audio, and render and export the result. Support human-in-the-loop (HITL) collaboration by making agent-created edits visible, adjustable, and reversible in the same workspace used for manual editing.

## 2. Product Direction

Introduce a distinct **composition workspace** within Atelier while retaining the existing **single-clip editor**.

- The clip editor serves the intent: “Improve this video.”
- The composition workspace serves the intent: “Assemble a finished video.”

Both workspaces should share visual language and reuse relevant editing controls. Users should be able to move an edited clip into a composition without manually exporting and reimporting it.

The composition workspace must support projects containing a single clip. Adding a second clip must not unexpectedly change the interface or convert an existing clip-edit session.

## 3. Goals

1. Assemble video clips in a chosen order.
2. Configure transitions at clip boundaries.
3. Add, position, trim, detach, and export audio.
4. Adjust clip and audio-track volume, including changes over time.
5. Preview the assembled video before export.
6. Preserve an editable project independently of rendered outputs.
7. Allow the agent and user to collaborate on the same composition.
8. Keep quick, single-clip editing lightweight.

## 4. MVP Scope

### Included

- One sequential video lane.
- Original audio associated with each video clip.
- Multiple additional audio lanes.
- Clip ordering, trimming, and access to existing reframe tools.
- Hard cuts and dissolve transitions with configurable duration.
- Audio placement, trimming, volume, mute, and fades.
- Volume automation sufficient to lower music during dialogue.
- Audio detachment and audio-only export.
- Manual and agent-assisted editing.
- Project persistence, undo/redo, preview, rendering, and export.

### Deferred

- Overlapping video layers, picture-in-picture, and compositing.
- Titles, motion graphics, and visual effects beyond existing clip tools.
- Multicamera editing.
- Advanced color grading and audio processing.
- Real-time collaboration between multiple people.
- A chat surface inside the workspace and selection-carrying chat requests (journey 5.4, HL-06). MVP agent interaction happens conversation-side.
- Agent pre-assembly of compositions (chosen order and transitions) and proposal batches (HL-03–HL-05, HL-07–HL-09). The MVP composer intent opens the workspace seeded with referenced media only, and every edit is manual.
- Automatic dialogue detection and automatic music ducking. The MVP may use user- or agent-specified time ranges for volume automation.

## 5. Core User Journeys

### 5.1 Assemble clips from a conversation

1. The user chooses **New composition** from a conversation.
2. Atelier opens the composition workspace; its media picker lists the conversation's video and audio artifacts, widening to the owning library when the conversation belongs to a project (the subtitle editor's linked-media scope), with rows labeled by their source chat.
3. The user adds clips from the picker; they land on the timeline in the order added, and the order remains editable there.
4. The user previews the sequence, reorders clips, and adjusts boundaries.
5. The user adds transitions and audio as needed.
6. The user exports the finished video as a new artifact.

### 5.2 Move from clip editing to composition

1. The user opens a video in the existing clip editor.
2. The user trims or reframes the clip.
3. The user chooses **Use in composition**.
4. Atelier offers a new or existing composition as the destination.
5. The edited result becomes available in that composition without manual file transfer.

### 5.3 Review an agent-created composition

1. The user asks the agent to assemble selected or referenced media.
2. The agent creates a draft composition or proposes changes to an existing one.
3. Atelier shows the timeline and a readable summary of proposed changes.
4. The user previews, adjusts, accepts, or rejects the proposal.
5. Acceptance updates the editable project without requiring a final export.

### 5.4 Refine a selected region through chat (v2)

Deferred from the MVP: it requires a chat surface inside the workspace. Target flow for v2:

1. The user selects a clip, boundary, audio segment, or time range.
2. The user requests a change, such as “Make this dissolve shorter” or “Lower the music here.”
3. The agent proposes an edit scoped to that selection.
4. The user reviews the change in context and accepts or rejects it.

## 6. Workspace and Navigation

### Entry Points

| Context | Action | Outcome |
|---|---|---|
| Individual video artifact | Edit video | Opens the existing clip editor |
| Conversation | New composition | Opens the composition workspace; media is selected inside it via the picker |
| Chat | “Open the video composer” | Opens the composition workspace via the editor-launch intent, seeded with any referenced media |
| Existing clip editor | Use in composition | Adds the edited result to a new or existing project |
| Saved composition | Open composition | Restores its editable timeline and media references |

**Chat routing.** A chat ask opens the composition workspace only through an explicit composer intent — wording like “open the video composer” or “make a composition of these,” routed the way the subtitle and markdown editors open from chat today. An intent that names media seeds the composition with it: seeded media land on the timeline in the order referenced, exactly as if the user had picked them from the picker in that order — the agent chooses no order, transitions, or settings in the MVP (pre-assembled drafts are a v2 goal). Join-style asks without the explicit intent (“join these clips,” “put these together”) keep routing to the existing one-shot join tools, so the agent never silently chooses between a flat rendered join and an editable composition.

### Workspace Layout

| Area | Required behavior |
|---|---|
| Media bin | Lists the project's media and adds to it through an in-workspace picker of conversation and library video/audio artifacts (the subtitle editor's linked-media scope and source labels) |
| Preview | Plays the assembled sequence at the current timeline position |
| Timeline | Shows clip order, transitions, original audio, additional audio lanes, and playhead |
| Inspector | Shows controls relevant to the selected item |
| Project and export controls | Provide project naming, save status, export configuration, and render status |

Selection must determine the inspector contents. Detailed settings should not obscure basic sequencing and playback controls.

## 7. Functional Requirements

### 7.1 Projects and Media

- **PR-01:** A composition must be a persistent editable project, distinct from its source media and rendered exports.
- **PR-02:** Users must be able to name, save, reopen, and duplicate a project.
- **PR-03:** Projects must preserve clip order, source ranges, transitions, audio placement, volume settings, and output settings.
- **PR-04:** Users must be able to add media from Atelier conversations and local files.
- **PR-05:** Original source media must remain unchanged by composition edits.
- **PR-06:** Reopening a project with unavailable source media must identify the affected items and allow relinking or replacement without discarding the project.
- **PR-07:** The workspace must communicate whether project changes are saved.

### 7.2 Video Sequencing

- **VS-01:** Users must be able to add, remove, duplicate, and reorder video clips on one sequential video lane.
- **VS-02:** Users must be able to adjust each clip’s source in/out points and inspect its resulting duration.
- **VS-03:** Existing trim and reframe capabilities must be accessible from the composition workflow.
- **VS-04:** Reordering or trimming clips must update sequence duration and transition placement predictably.
- **VS-05:** Users must be able to undo and redo timeline edits.
- **VS-06:** Clips with differing dimensions, aspect ratios, or frame rates must use explicit project output settings. Users must be able to inspect framing before export.
- **VS-07:** Reordering a clip must move its associated original audio with it. Independently placed audio must retain its timeline position unless the user chooses to move it.

### 7.3 Transitions

- **TR-01:** Every adjacent pair of video clips must expose a selectable boundary.
- **TR-02:** The MVP must support a hard cut and a dissolve.
- **TR-03:** Users must be able to set, change, and remove a transition at a boundary.
- **TR-04:** Dissolve duration must be adjustable and visible on the timeline.
- **TR-05:** Atelier must prevent invalid transition durations and explain any adjustment required by available clip lengths.
- **TR-06:** The timeline must display the resulting sequence duration, including transition overlap.
- **TR-07:** Transition settings must make the treatment of original clip audio clear, including whether it cuts or crossfades.

### 7.4 Audio Editing and Mixing

- **AU-01:** Users must be able to add multiple audio lanes and position audio segments against the video timeline.
- **AU-02:** Users must be able to trim, move, duplicate, and remove audio segments.
- **AU-03:** Users must be able to adjust individual segment gain and overall lane volume, mute lanes or segments, and temporarily solo a lane for preview.
- **AU-04:** Original video audio must expose volume and mute controls without requiring detachment.
- **AU-05:** Users must be able to apply audio fade-in and fade-out durations.
- **AU-06:** Users must be able to vary audio volume over time, with visible editable points or ranges, to lower music during dialogue.
- **AU-07:** Users must be able to detach a video clip’s audio into an independently editable audio lane. Detachment must preserve timing and prevent duplicate playback of the same sound.
- **AU-08:** Users must be able to export a selected audio segment or the composition’s final audio mix as an audio file.
- **AU-09:** Detach audio, export audio, and mute audio must be separate, clearly named actions.
- **AU-10:** The workspace must expose an output level meter or equivalent indication of audio clipping.
- **AU-11:** Users must be able to choose whether export includes the audio mix. Audio extending beyond the video sequence must be visibly identified; the default video export ends at the video sequence boundary.

### 7.5 Preview

- **PV-01:** Users must be able to play, pause, seek, and scrub the assembled sequence (the rendered preview file's native controls).
- **PV-02 (v1):** Preview is a render of the working timeline, on demand — accurate by construction, because the preview IS the render (trims, dissolves, the audio mix, ducks, and the output frame all appear exactly as export produces them). No forced save precedes it. Interactive in-timeline scrubbing arrives with v2.
- **PV-03:** Users must be able to preview a selected range (the render controls' From/To).
- **PV-04:** Superseded by the v1 approach — nothing is approximated.
- **PV-05:** Satisfied by the same mechanism (any render is the real graph).
- **PV-06:** Render progress shows as a percentage with cancellation, and never loses project edits.

### 7.6 Human-in-the-Loop Collaboration

- **HL-01:** Agent-assisted edits must target the same editable project and controls available to manual editing.
- **HL-02:** The explicit composer intent must create a composition seeded with the referenced media — seeded only, in reference order (see Chat routing in section 6). Agent pre-assembly of the draft (chosen order, transitions, settings) is a v2 goal.
- **HL-03 (v2):** Agent-proposed changes must appear as a reviewable batch with a readable summary and visible timeline changes.
- **HL-04 (v2):** Users must be able to preview, accept, or reject a proposed batch. Accepted changes must be undoable as a batch.
- **HL-05 (v2):** Users must be able to adjust a proposal through normal editor controls before accepting it.
- **HL-06 (v2):** Chat requests must carry the relevant editor selection so references such as “this clip” and “this transition” resolve in context. Deferred with the in-workspace chat surface.
- **HL-07 (v2):** The agent must not silently overwrite manual edits made after its proposal began. A stale proposal must be refreshed or reconciled before acceptance. (Moot in v1: the agent cannot mutate a project — seeded-only.)
- **HL-08:** Accepting a proposal must not require a final render or export.
- **HL-09:** Failed or rejected proposals must preserve the previously accepted project state.

### 7.7 Rendering and Export

- **EX-01:** Saving a project and exporting media must be separate actions.
- **EX-02:** Export must provide a simple default preset, with resolution, frame rate, format, and quality available as advanced settings.
- **EX-03:** Export must display the output dimensions, estimated duration, and whether audio is included before rendering starts.
- **EX-04:** Each render must use a saved, immutable project revision. Subsequent edits must not alter an export already underway.
- **EX-05:** Rendering must show queued, running, completed, failed, and cancelled states as applicable.
- **EX-06:** Users must be able to cancel a render and retry a failed render without rebuilding the composition.
- **EX-07:** Completed exports must become new Atelier artifacts linked to their source composition and revision.
- **EX-08:** Users must be able to save completed exports to a chosen local destination.
- **EX-09:** Export failure messages must identify the actionable cause where known and preserve the editable project.

## 8. Interaction Principles

1. **Keep simple edits simple.** Opening one video for a quick trim should retain the existing lightweight workflow.
2. **Make consequences visible.** Show how transitions, trims, and audio changes affect timing and output.
3. **Preserve reversibility.** Editing should preserve source media and support undo/redo.
4. **Use progressive disclosure.** Reveal advanced controls through selection and expandable export settings.
5. **Make agent decisions inspectable.** Users should review concrete timeline edits rather than opaque rendering instructions.
6. **Preserve continuity.** Users should move between conversation, clip editing, composition, and export without manual file shuffling.

## 9. MVP Acceptance Criteria

| Scenario | Acceptance criterion |
|---|---|
| Create a sequence | A composition created from a conversation adds three video artifacts through the picker into a visible, editable order |
| Edit a sequence | Reordering and trimming clips update preview and total duration; undo restores the prior state |
| Configure a transition | A dissolve can be added, resized within valid limits, previewed, and removed |
| Handle mixed source sizes | Differently sized clips conform to the chosen output frame, with framing inspectable before export |
| Add music | An imported audio file can be placed, trimmed, faded, muted, and mixed with original clip audio |
| Lower music during dialogue | Volume can be reduced over a specified range and restored afterward; the change is audible in preview and export |
| Detach audio | Detached audio remains synchronized initially and plays once, with independent editing controls |
| Export audio | A selected segment or final mix exports as a playable audio artifact |
| Review agent edits (v2) | A proposed batch shows its changes, can be previewed and adjusted, and supports acceptance, rejection, and undo — deferred with pre-assembly |
| Protect concurrent edits (v2) | An agent proposal based on an older project revision cannot silently replace newer manual edits — moot in v1 (the agent cannot mutate a project) |
| Reopen a project | Saved timeline, transitions, audio settings, and output settings are restored |
| Recover missing media | A missing source is identified and can be relinked without recreating the timeline |
| Export a video | The completed artifact reflects the saved revision’s clip order, timing, transitions, framing, and audio mix |
| Edit during rendering | Changes made after export begins do not change that export’s output |
| Recover from render failure | Failure or cancellation preserves the project and allows another export attempt |
| Preserve quick editing | Edit video continues to open the existing single-clip workflow |
| Open the composer by chat intent | "Open the video composer" in a conversation with attached or mentioned media opens the workspace seeded with that media, in reference order; with none, it opens unseeded with the picker |

## 10. Decisions to Resolve During Design

- Performance targets and supported project limits, including clip count, duration, resolution, and audio-lane count.
- An OpenTimelineIO import/export adapter over the document's OTIO-shaped vocabulary (the native format deliberately mirrors it).

Resolved during implementation: compositions are conversation-attached `cmp_` artifacts (every save an immutable revision; reopen, duplicate, and export pinning all read them); saves are explicit; the output frame inherits the first clip's display size and frame rate with pad as the default fit, overridable per project and per export; the default dissolve is 1s with an equal-power audio crossfade; detached audio starts pinned to its clip then moves independently; export ships MP4/H.264 (CRF 18/23/28) with an audio-only M4A path; the composer intent is seeded-only, and project-mutating agent tools are deferred to v2 with the proposal batches.

## 11. Validation Focus

Evaluate the design with representative tasks: assemble three clips, adjust a dissolve, add music under dialogue, detach and export audio, review an agent proposal, and export a finished video.

Confirm that users understand the distinction between clip editing and composition, can predict timeline changes, and can identify what the agent changed. Define measurable usability and preview-performance targets before implementation is considered release-ready.
