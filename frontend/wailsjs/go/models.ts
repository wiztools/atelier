export namespace main {
	
	export class ConfigUI {
	    mode: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigUI(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	    }
	}
	export class ConfigFilesystemTool {
	    root: string;
	    maxOutputBytes: number;
	    timeoutMs: number;
	    allowedCommands: string[];
	
	    static createFrom(source: any = {}) {
	        return new ConfigFilesystemTool(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.root = source["root"];
	        this.maxOutputBytes = source["maxOutputBytes"];
	        this.timeoutMs = source["timeoutMs"];
	        this.allowedCommands = source["allowedCommands"];
	    }
	}
	export class ConfigTools {
	    filesystem: ConfigFilesystemTool;
	
	    static createFrom(source: any = {}) {
	        return new ConfigTools(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filesystem = this.convertValues(source["filesystem"], ConfigFilesystemTool);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConfigVideoGeneration {
	    duration: string;
	    aspectRatio: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigVideoGeneration(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.duration = source["duration"];
	        this.aspectRatio = source["aspectRatio"];
	    }
	}
	export class ConfigImageGeneration {
	    width: number;
	    height: number;
	    steps: number;
	
	    static createFrom(source: any = {}) {
	        return new ConfigImageGeneration(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.width = source["width"];
	        this.height = source["height"];
	        this.steps = source["steps"];
	    }
	}
	export class ConfigGeneration {
	    image: ConfigImageGeneration;
	    video: ConfigVideoGeneration;
	
	    static createFrom(source: any = {}) {
	        return new ConfigGeneration(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.image = this.convertValues(source["image"], ConfigImageGeneration);
	        this.video = this.convertValues(source["video"], ConfigVideoGeneration);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConfigPrompts {
	    system: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigPrompts(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.system = source["system"];
	    }
	}
	export class ConfigModels {
	    primaryProvider?: string;
	    harnessProvider?: string;
	    imageProvider?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigModels(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.primaryProvider = source["primaryProvider"];
	        this.harnessProvider = source["harnessProvider"];
	        this.imageProvider = source["imageProvider"];
	    }
	}
	export class ConfigFal {
	    enabled: boolean;
	    model?: string;
	    imageEditModel?: string;
	    videoModel?: string;
	    videoImageModel?: string;
	    audioModel?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigFal(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.model = source["model"];
	        this.imageEditModel = source["imageEditModel"];
	        this.videoModel = source["videoModel"];
	        this.videoImageModel = source["videoImageModel"];
	        this.audioModel = source["audioModel"];
	    }
	}
	export class ConfigOpenRouter {
	    enabled: boolean;
	    primary?: string;
	    harness?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigOpenRouter(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.primary = source["primary"];
	        this.harness = source["harness"];
	    }
	}
	export class ConfigOllamaModels {
	    primary: string;
	    harness: string;
	    image: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigOllamaModels(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.primary = source["primary"];
	        this.harness = source["harness"];
	        this.image = source["image"];
	    }
	}
	export class ConfigOllama {
	    baseURL: string;
	    models: ConfigOllamaModels;
	    numCtx: number;
	
	    static createFrom(source: any = {}) {
	        return new ConfigOllama(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.baseURL = source["baseURL"];
	        this.models = this.convertValues(source["models"], ConfigOllamaModels);
	        this.numCtx = source["numCtx"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConfigProviders {
	    ollama: ConfigOllama;
	    openrouter: ConfigOpenRouter;
	    fal: ConfigFal;
	
	    static createFrom(source: any = {}) {
	        return new ConfigProviders(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ollama = this.convertValues(source["ollama"], ConfigOllama);
	        this.openrouter = this.convertValues(source["openrouter"], ConfigOpenRouter);
	        this.fal = this.convertValues(source["fal"], ConfigFal);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConfigStorage {
	    root: string;
	    history: string;
	    artifacts: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigStorage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.root = source["root"];
	        this.history = source["history"];
	        this.artifacts = source["artifacts"];
	    }
	}
	export class AppConfig {
	    version: number;
	    storage: ConfigStorage;
	    providers: ConfigProviders;
	    models: ConfigModels;
	    prompts: ConfigPrompts;
	    generation: ConfigGeneration;
	    tools: ConfigTools;
	    ui: ConfigUI;
	
	    static createFrom(source: any = {}) {
	        return new AppConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.storage = this.convertValues(source["storage"], ConfigStorage);
	        this.providers = this.convertValues(source["providers"], ConfigProviders);
	        this.models = this.convertValues(source["models"], ConfigModels);
	        this.prompts = this.convertValues(source["prompts"], ConfigPrompts);
	        this.generation = this.convertValues(source["generation"], ConfigGeneration);
	        this.tools = this.convertValues(source["tools"], ConfigTools);
	        this.ui = this.convertValues(source["ui"], ConfigUI);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ollamaToolFunction {
	    name: string;
	    arguments: number[];
	
	    static createFrom(source: any = {}) {
	        return new ollamaToolFunction(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.arguments = source["arguments"];
	    }
	}
	export class ollamaToolCall {
	    type?: string;
	    function: ollamaToolFunction;
	
	    static createFrom(source: any = {}) {
	        return new ollamaToolCall(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.function = this.convertValues(source["function"], ollamaToolFunction);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChatMessage {
	    role: string;
	    content: string;
	    images?: string[];
	    tool_calls?: ollamaToolCall[];
	
	    static createFrom(source: any = {}) {
	        return new ChatMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.role = source["role"];
	        this.content = source["content"];
	        this.images = source["images"];
	        this.tool_calls = this.convertValues(source["tool_calls"], ollamaToolCall);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChatRequest {
	    requestID?: string;
	    conversationId?: string;
	    baseURL?: string;
	    provider?: string;
	    model: string;
	    selectedModel?: string;
	    system?: string;
	    messages: ChatMessage[];
	    think?: any;
	    options?: Record<string, any>;
	    format?: any;
	    tools?: any[];
	
	    static createFrom(source: any = {}) {
	        return new ChatRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requestID = source["requestID"];
	        this.conversationId = source["conversationId"];
	        this.baseURL = source["baseURL"];
	        this.provider = source["provider"];
	        this.model = source["model"];
	        this.selectedModel = source["selectedModel"];
	        this.system = source["system"];
	        this.messages = this.convertValues(source["messages"], ChatMessage);
	        this.think = source["think"];
	        this.options = source["options"];
	        this.format = source["format"];
	        this.tools = source["tools"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChatStreamStart {
	    requestID: string;
	    conversationId: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatStreamStart(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requestID = source["requestID"];
	        this.conversationId = source["conversationId"];
	    }
	}
	
	
	
	
	
	
	
	
	
	
	
	
	
	
	export class HistoryContent {
	    type: string;
	    text?: string;
	    artifactId?: string;
	    path?: string;
	    mimeType?: string;
	    width?: number;
	    height?: number;
	
	    static createFrom(source: any = {}) {
	        return new HistoryContent(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.text = source["text"];
	        this.artifactId = source["artifactId"];
	        this.path = source["path"];
	        this.mimeType = source["mimeType"];
	        this.width = source["width"];
	        this.height = source["height"];
	    }
	}
	export class HistoryTurn {
	    schemaVersion: number;
	    id: string;
	    conversationId: string;
	    createdAt: string;
	    kind: string;
	    role: string;
	    model?: string;
	    provider?: string;
	    content: HistoryContent[];
	    request?: Record<string, any>;
	    providerResponse?: Record<string, any>;
	    deletedAt?: string;
	
	    static createFrom(source: any = {}) {
	        return new HistoryTurn(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.schemaVersion = source["schemaVersion"];
	        this.id = source["id"];
	        this.conversationId = source["conversationId"];
	        this.createdAt = source["createdAt"];
	        this.kind = source["kind"];
	        this.role = source["role"];
	        this.model = source["model"];
	        this.provider = source["provider"];
	        this.content = this.convertValues(source["content"], HistoryContent);
	        this.request = source["request"];
	        this.providerResponse = source["providerResponse"];
	        this.deletedAt = source["deletedAt"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class HistoryConversationStats {
	    turnCount: number;
	    artifactCount: number;
	
	    static createFrom(source: any = {}) {
	        return new HistoryConversationStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.turnCount = source["turnCount"];
	        this.artifactCount = source["artifactCount"];
	    }
	}
	export class HistoryDefaults {
	    chatModel?: string;
	    imageModel?: string;
	    system?: string;
	
	    static createFrom(source: any = {}) {
	        return new HistoryDefaults(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.chatModel = source["chatModel"];
	        this.imageModel = source["imageModel"];
	        this.system = source["system"];
	    }
	}
	export class HistoryProvider {
	    id: string;
	    baseURL: string;
	
	    static createFrom(source: any = {}) {
	        return new HistoryProvider(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.baseURL = source["baseURL"];
	    }
	}
	export class HistoryConversation {
	    schemaVersion: number;
	    id: string;
	    kind: string;
	    title: string;
	    createdAt: string;
	    updatedAt: string;
	    deletedAt?: string;
	    provider: HistoryProvider;
	    defaults: HistoryDefaults;
	    stats: HistoryConversationStats;
	
	    static createFrom(source: any = {}) {
	        return new HistoryConversation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.schemaVersion = source["schemaVersion"];
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.title = source["title"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	        this.deletedAt = source["deletedAt"];
	        this.provider = this.convertValues(source["provider"], HistoryProvider);
	        this.defaults = this.convertValues(source["defaults"], HistoryDefaults);
	        this.stats = this.convertValues(source["stats"], HistoryConversationStats);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConversationDetail {
	    conversation: HistoryConversation;
	    turns: HistoryTurn[];
	
	    static createFrom(source: any = {}) {
	        return new ConversationDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversation = this.convertValues(source["conversation"], HistoryConversation);
	        this.turns = this.convertValues(source["turns"], HistoryTurn);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConversationSummary {
	    id: string;
	    kind: string;
	    title: string;
	    createdAt: string;
	    updatedAt: string;
	    deletedAt?: string;
	    turnCount: number;
	    artifactCount: number;
	
	    static createFrom(source: any = {}) {
	        return new ConversationSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.title = source["title"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	        this.deletedAt = source["deletedAt"];
	        this.turnCount = source["turnCount"];
	        this.artifactCount = source["artifactCount"];
	    }
	}
	export class FalModel {
	    id: string;
	    displayName: string;
	    category: string;
	    description: string;
	    status: string;
	    tags: string[];
	    thumbnailUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new FalModel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.displayName = source["displayName"];
	        this.category = source["category"];
	        this.description = source["description"];
	        this.status = source["status"];
	        this.tags = source["tags"];
	        this.thumbnailUrl = source["thumbnailUrl"];
	    }
	}
	export class HarnessToolCall {
	    name: string;
	    command?: string;
	    args?: string[];
	    cwd?: string;
	    env?: Record<string, string>;
	    timeoutMs?: number;
	    path?: string;
	    content?: string;
	    model?: string;
	    append?: boolean;
	    overwrite?: boolean;
	    maxBytes?: number;
	    allowBinary?: boolean;
	    negativePrompt?: string;
	    generateAudio?: boolean;
	    aspectRatio?: string;
	    duration?: string;
	
	    static createFrom(source: any = {}) {
	        return new HarnessToolCall(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.command = source["command"];
	        this.args = source["args"];
	        this.cwd = source["cwd"];
	        this.env = source["env"];
	        this.timeoutMs = source["timeoutMs"];
	        this.path = source["path"];
	        this.content = source["content"];
	        this.model = source["model"];
	        this.append = source["append"];
	        this.overwrite = source["overwrite"];
	        this.maxBytes = source["maxBytes"];
	        this.allowBinary = source["allowBinary"];
	        this.negativePrompt = source["negativePrompt"];
	        this.generateAudio = source["generateAudio"];
	        this.aspectRatio = source["aspectRatio"];
	        this.duration = source["duration"];
	    }
	}
	export class HarnessToolResult {
	    name: string;
	    status: string;
	    summary: string;
	    result?: any;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new HarnessToolResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.status = source["status"];
	        this.summary = source["summary"];
	        this.result = source["result"];
	        this.error = source["error"];
	    }
	}
	
	
	
	
	
	
	export class ModelInfo {
	    provider: string;
	    id: string;
	    displayName: string;
	    contextLength?: number;
	    capabilities?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ModelInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.id = source["id"];
	        this.displayName = source["displayName"];
	        this.contextLength = source["contextLength"];
	        this.capabilities = source["capabilities"];
	    }
	}
	export class OllamaModel {
	    name: string;
	    modifiedAt?: string;
	    size?: number;
	    family?: string;
	    parameter?: string;
	    capabilities?: string[];
	    imageGeneration?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new OllamaModel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.modifiedAt = source["modifiedAt"];
	        this.size = source["size"];
	        this.family = source["family"];
	        this.parameter = source["parameter"];
	        this.capabilities = source["capabilities"];
	        this.imageGeneration = source["imageGeneration"];
	    }
	}
	export class OllamaStatus {
	    online: boolean;
	    version?: string;
	    baseURL: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new OllamaStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.online = source["online"];
	        this.version = source["version"];
	        this.baseURL = source["baseURL"];
	        this.error = source["error"];
	    }
	}
	export class PurgeArchivedResult {
	    deletedConversations: number;
	    deletedAssets: number;
	
	    static createFrom(source: any = {}) {
	        return new PurgeArchivedResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deletedConversations = source["deletedConversations"];
	        this.deletedAssets = source["deletedAssets"];
	    }
	}
	export class SaveAudioRequest {
	    path: string;
	    suggestedName?: string;
	
	    static createFrom(source: any = {}) {
	        return new SaveAudioRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.suggestedName = source["suggestedName"];
	    }
	}
	export class SaveImageRequest {
	    image: string;
	    suggestedName?: string;
	
	    static createFrom(source: any = {}) {
	        return new SaveImageRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.image = source["image"];
	        this.suggestedName = source["suggestedName"];
	    }
	}
	export class SaveVideoRequest {
	    path: string;
	    suggestedName?: string;
	
	    static createFrom(source: any = {}) {
	        return new SaveVideoRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.suggestedName = source["suggestedName"];
	    }
	}
	export class ToolCommandRequest {
	    command: string;
	    args?: string[];
	    cwd?: string;
	    env?: Record<string, string>;
	    timeoutMs?: number;
	
	    static createFrom(source: any = {}) {
	        return new ToolCommandRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.command = source["command"];
	        this.args = source["args"];
	        this.cwd = source["cwd"];
	        this.env = source["env"];
	        this.timeoutMs = source["timeoutMs"];
	    }
	}
	export class ToolCommandResult {
	    command: string[];
	    cwd: string;
	    exitCode: number;
	    stdout?: string;
	    stderr?: string;
	    durationMs: number;
	    truncated?: boolean;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolCommandResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.command = source["command"];
	        this.cwd = source["cwd"];
	        this.exitCode = source["exitCode"];
	        this.stdout = source["stdout"];
	        this.stderr = source["stderr"];
	        this.durationMs = source["durationMs"];
	        this.truncated = source["truncated"];
	        this.error = source["error"];
	    }
	}
	export class ToolExecutionRequest {
	    name: string;
	    call: HarnessToolCall;
	    requestId?: string;
	    conversationId?: string;
	    source?: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolExecutionRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.call = this.convertValues(source["call"], HarnessToolCall);
	        this.requestId = source["requestId"];
	        this.conversationId = source["conversationId"];
	        this.source = source["source"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ToolFileEntry {
	    name: string;
	    path: string;
	    isDir: boolean;
	    size: number;
	
	    static createFrom(source: any = {}) {
	        return new ToolFileEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.isDir = source["isDir"];
	        this.size = source["size"];
	    }
	}
	export class ToolFileListRequest {
	    path?: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolFileListRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	    }
	}
	export class ToolFileListResult {
	    root: string;
	    path: string;
	    entries: ToolFileEntry[];
	
	    static createFrom(source: any = {}) {
	        return new ToolFileListResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.root = source["root"];
	        this.path = source["path"];
	        this.entries = this.convertValues(source["entries"], ToolFileEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ToolFileReadRequest {
	    path: string;
	    maxBytes?: number;
	    allowBinary?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ToolFileReadRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.maxBytes = source["maxBytes"];
	        this.allowBinary = source["allowBinary"];
	    }
	}
	export class ToolFileReadResult {
	    path: string;
	    content: string;
	    bytes: number;
	    truncated?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ToolFileReadResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.content = source["content"];
	        this.bytes = source["bytes"];
	        this.truncated = source["truncated"];
	    }
	}
	export class ToolFileWriteRequest {
	    path: string;
	    content: string;
	    append?: boolean;
	    overwrite?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ToolFileWriteRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.content = source["content"];
	        this.append = source["append"];
	        this.overwrite = source["overwrite"];
	    }
	}
	export class ToolFileWriteResult {
	    path: string;
	    bytes: number;
	
	    static createFrom(source: any = {}) {
	        return new ToolFileWriteResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.bytes = source["bytes"];
	    }
	}
	

}

