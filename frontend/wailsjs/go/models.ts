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
	
	    static createFrom(source: any = {}) {
	        return new ConfigGeneration(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.image = this.convertValues(source["image"], ConfigImageGeneration);
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
	export class ConfigOllamaModels {
	    chat: string;
	    image: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigOllamaModels(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.chat = source["chat"];
	        this.image = source["image"];
	    }
	}
	export class ConfigOllama {
	    baseURL: string;
	    models: ConfigOllamaModels;
	
	    static createFrom(source: any = {}) {
	        return new ConfigOllama(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.baseURL = source["baseURL"];
	        this.models = this.convertValues(source["models"], ConfigOllamaModels);
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
	
	    static createFrom(source: any = {}) {
	        return new ConfigProviders(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ollama = this.convertValues(source["ollama"], ConfigOllama);
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
	    prompts: ConfigPrompts;
	    generation: ConfigGeneration;
	    ui: ConfigUI;
	
	    static createFrom(source: any = {}) {
	        return new AppConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.storage = this.convertValues(source["storage"], ConfigStorage);
	        this.providers = this.convertValues(source["providers"], ConfigProviders);
	        this.prompts = this.convertValues(source["prompts"], ConfigPrompts);
	        this.generation = this.convertValues(source["generation"], ConfigGeneration);
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
	export class ChatMessage {
	    role: string;
	    content: string;
	    images?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ChatMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.role = source["role"];
	        this.content = source["content"];
	        this.images = source["images"];
	    }
	}
	export class ChatRequest {
	    requestID?: string;
	    conversationId?: string;
	    baseURL?: string;
	    model: string;
	    system?: string;
	    messages: ChatMessage[];
	    think?: any;
	    options?: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new ChatRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requestID = source["requestID"];
	        this.conversationId = source["conversationId"];
	        this.baseURL = source["baseURL"];
	        this.model = source["model"];
	        this.system = source["system"];
	        this.messages = this.convertValues(source["messages"], ChatMessage);
	        this.think = source["think"];
	        this.options = source["options"];
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
	
	
	
	
	
	
	export class ImageGenerateRequest {
	    baseURL?: string;
	    model: string;
	    prompt: string;
	    width?: number;
	    height?: number;
	    steps?: number;
	
	    static createFrom(source: any = {}) {
	        return new ImageGenerateRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.baseURL = source["baseURL"];
	        this.model = source["model"];
	        this.prompt = source["prompt"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.steps = source["steps"];
	    }
	}
	export class ImageGenerateResponse {
	    model?: string;
	    text?: string;
	    images: string[];
	    raw?: string;
	    error?: string;
	    conversationId?: string;
	
	    static createFrom(source: any = {}) {
	        return new ImageGenerateResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.text = source["text"];
	        this.images = source["images"];
	        this.raw = source["raw"];
	        this.error = source["error"];
	        this.conversationId = source["conversationId"];
	    }
	}
	export class OllamaModel {
	    name: string;
	    modifiedAt?: string;
	    size?: number;
	    family?: string;
	    parameter?: string;
	
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

}

