// Injected ahead of the app: fake Wails bindings and runtime over a small,
// fixed workspace, so the frontend boots with no Go backend — no config
// read, no keychain, no network. Every binding call is recorded in order;
// event listeners are recorded so the runner can replay backend events.
//
// The fake data is the contract the session depends on: conv_alpha's history
// carries an image, a transcript, a markdown document, and a completed run
// with media usage (so the usage tables, chips, and panels all render), the
// markdown chip resolves through ResolveMarkdownEditSource so the editor
// opens, and the search returns the wire shape ({conversation, titleMatched,
// matches}). Extend a fixture when the session grows a step that needs it.
(() => {
  const at = (day) => `2026-11-0${day}T10:00:00Z`;
  const conversations = [
    {id: 'conv_alpha', kind: 'chat', title: 'Storyboard ideas', createdAt: at(1), updatedAt: at(3), turnCount: 4, artifactCount: 2},
    {id: 'conv_beta', kind: 'chat', title: 'Poster draft', createdAt: at(1), updatedAt: at(2), turnCount: 2, artifactCount: 0, projectId: 'proj_1'},
  ];
  const libraries = [{
    id: 'lib_1', name: 'Studio', createdAt: at(1), updatedAt: at(1),
    projects: [{id: 'proj_1', libraryId: 'lib_1', name: 'Campaign', notes: 'Render video at 9:16.', createdAt: at(1), updatedAt: at(1)}],
  }];
  const assets = [
    {id: 'img_0123456789abcdef', conversationId: 'conv_alpha', kind: 'image', mimeType: 'image/png', url: '/atelier-artifact/conv_alpha/img_0123456789abcdef.png', width: 1024, height: 1024, originTurnId: 'turn_000002', role: 'assistant', createdAt: at(2)},
  ];
  const run = {
    status: 'completed', durationMs: 7300,
    steps: [
      {kind: 'triage', provider: 'ollama', model: 'qwen3:8b', promptTokens: 1200, tokens: 40, durationMs: 800},
      {kind: 'tool_call', durationMs: 4000, summary: 'running generate_image', tools: [
        {name: 'generate_image', status: 'completed', provider: 'fal', model: 'fal-ai/flux/schnell', mediaKind: 'image', mediaCount: 1, costMicros: 3000, durationMs: 3900},
        {name: 'transcribe_audio', status: 'completed', provider: 'replicate', model: 'openai/whisper', costUnknown: true, path: '/Users/ana/Shared/clip.m4a'},
      ]},
      {kind: 'model_call', provider: 'openrouter', model: 'anthropic/claude', promptTokens: 5000, tokens: 300, costMicros: 15000, durationMs: 2000, firstTokenMs: 500},
    ],
  };
  const turn = (id, role, content, extra = {}) => ({schemaVersion: 1, id, conversationId: 'conv_alpha', createdAt: at(2), kind: 'message', role, content, ...extra});
  const details = {
    conv_alpha: {
      conversation: {schemaVersion: 1, id: 'conv_alpha', kind: 'chat', title: 'Storyboard ideas', createdAt: at(1), updatedAt: at(3), provider: {}, defaults: {}, stats: {}},
      turns: [
        turn('turn_000001', 'user', [{type: 'text', text: 'Make a poster from @img_01234567'}]),
        turn('turn_000002', 'assistant', [
          {type: 'text', text: 'Here is the **poster**.'},
          {type: 'image', text: '/atelier-artifact/conv_alpha/img_0123456789abcdef.png'},
        ], {provider: 'openrouter', providerResponse: {harnessRun: run}}),
        turn('turn_000003', 'user', [{type: 'text', text: 'Transcribe the clip'}]),
        turn('turn_000004', 'assistant', [
          {type: 'text', text: 'Done — the transcript is attached.'},
          {type: 'transcript', text: '/atelier-artifact/conv_alpha/trc_0a1b2c3d.vtt'},
          {type: 'markdown', text: '/atelier-artifact/conv_alpha/md_0a1b2c3d.md'},
        ], {provider: 'ollama'}),
      ],
    },
  };
  const listeners = {};
  const calls = [];
  window.__atelier = {
    calls, listeners, lastStream: null,
    emit(name, payload) {
      const cbs = listeners[name] || [];
      cbs.forEach((cb) => cb(payload));
      return cbs.length;
    },
  };
  const answers = {
    GetConfig: () => ({}),
    LoadUIState: () => ({sidebar: {chatsCollapsed: false, librariesCollapsed: false, expandedLibraries: ['lib_1'], expandedProjects: ['proj_1']}, settings: {lastTab: 'providers'}}),
    ListConversations: () => conversations,
    ListLibraries: () => libraries,
    ListProjectConversations: (id) => conversations.filter((c) => c.projectId === id),
    GetConversation: (id) => details[id] || {conversation: {schemaVersion: 1, id, kind: 'chat', title: 'New chat', createdAt: at(4), updatedAt: at(4), provider: {}, defaults: {}, stats: {}}, turns: []},
    ListConversationAssets: (id) => assets.filter((a) => a.conversationId === id),
    ListLibraryAssets: () => assets,
    CheckOllama: (url) => ({online: true, baseURL: url || 'http://localhost:11434', version: '0.6.0'}),
    ListModels: () => [{name: 'qwen3:8b', size: 5000000000, family: 'qwen3', parameter: '8B', capabilities: ['completion', 'tools']}],
    DetectLocalTools: () => ({binaries: [], transcriptionProvider: 'fal'}),
    StreamChat: (req) => {
      window.__atelier.lastStream = req;
      return {requestID: req.requestID, conversationId: req.conversationId || 'conv_new'};
    },
    SetConversationModelOverrides: (id) => ({...conversations[0], id, modelOverrides: {primaryProvider: 'ollama'}}),
    GetConversationUsageReport: (id) => ({
      scope: 'conversation', conversationId: id, title: 'Storyboard ideas',
      conversations: 1, turns: 4, costMicros: 18000, costUnknown: true,
      modelCalls: 2, toolCalls: 3, failedToolCalls: 1, mediaImages: 1, mediaVideos: 0, mediaAudios: 0,
      models: [{provider: 'ollama', model: 'qwen3:8b', calls: 1, promptTokens: 3600, completionTokens: 160, costMicros: 0, costUnknown: false, durationMs: 2300}],
      tools: [{name: 'generate_image', calls: 1, failures: 0, costMicros: 3000, costUnknown: false}],
    }),
    ResolveMarkdownEditSource: () => ({conversationId: 'conv_alpha', conversationTitle: 'Storyboard ideas', artifactId: 'md_0a1b2c3d', url: '/atelier-artifact/conv_alpha/md_0a1b2c3d.md', mimeType: 'text/markdown', format: 'markdown', text: '# Shot list\n\n- Opening wide on the city\n- Close-up on the poster\n', wordCount: 11}),
    SearchConversations: () => ({results: [{conversation: conversations[1], titleMatched: true, matches: []}], truncated: false}),
  };
  const call = (name) => (...args) => {
    calls.push(name);
    if (answers[name]) return Promise.resolve(JSON.parse(JSON.stringify(answers[name](...args) ?? null)));
    if (/^(List)/.test(name)) return Promise.resolve([]);
    if (/^Has/.test(name)) return Promise.resolve(false);
    return Promise.resolve(null);
  };
  window.go = {main: {App: new Proxy({}, {get: (_, name) => call(String(name))})}};
  window.runtime = new Proxy({}, {get: (_, name) => {
    if (name === 'EventsOnMultiple') {
      return (event, cb) => {
        (listeners[event] = listeners[event] || []).push(cb);
        return () => { listeners[event] = (listeners[event] || []).filter((x) => x !== cb); };
      };
    }
    if (name === 'EventsOff') return (event) => { delete listeners[event]; };
    if (name === 'Environment') return () => Promise.resolve({platform: 'darwin', arch: 'arm64', buildType: 'dev'});
    return () => undefined;
  }});
})();
