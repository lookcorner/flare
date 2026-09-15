export namespace daemon {
	
	export class APICheck {
	    reachable: boolean;
	    authed: boolean;
	    zones?: number;
	    latency_ms?: number;
	    err?: string;
	
	    static createFrom(source: any = {}) {
	        return new APICheck(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.reachable = source["reachable"];
	        this.authed = source["authed"];
	        this.zones = source["zones"];
	        this.latency_ms = source["latency_ms"];
	        this.err = source["err"];
	    }
	}
	export class CloudflaredCheck {
	    installed: boolean;
	    path?: string;
	    version?: string;
	    running: boolean;
	    pid?: number;
	
	    static createFrom(source: any = {}) {
	        return new CloudflaredCheck(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.installed = source["installed"];
	        this.path = source["path"];
	        this.version = source["version"];
	        this.running = source["running"];
	        this.pid = source["pid"];
	    }
	}
	export class RouteDiagnose {
	    name: string;
	    hostname: string;
	    service: string;
	    local_ok: boolean;
	    local_err?: string;
	    dns_ok: boolean;
	    dns_err?: string;
	    http_ok: boolean;
	    http_status?: number;
	    http_err?: string;
	    record_checked: boolean;
	    record_ok: boolean;
	    record_err?: string;
	
	    static createFrom(source: any = {}) {
	        return new RouteDiagnose(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.hostname = source["hostname"];
	        this.service = source["service"];
	        this.local_ok = source["local_ok"];
	        this.local_err = source["local_err"];
	        this.dns_ok = source["dns_ok"];
	        this.dns_err = source["dns_err"];
	        this.http_ok = source["http_ok"];
	        this.http_status = source["http_status"];
	        this.http_err = source["http_err"];
	        this.record_checked = source["record_checked"];
	        this.record_ok = source["record_ok"];
	        this.record_err = source["record_err"];
	    }
	}
	export class DiagnoseResult {
	    cloudflared: CloudflaredCheck;
	    api: APICheck;
	    routes: RouteDiagnose[];
	    total: number;
	    passed: number;
	    failed: number;
	
	    static createFrom(source: any = {}) {
	        return new DiagnoseResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cloudflared = this.convertValues(source["cloudflared"], CloudflaredCheck);
	        this.api = this.convertValues(source["api"], APICheck);
	        this.routes = this.convertValues(source["routes"], RouteDiagnose);
	        this.total = source["total"];
	        this.passed = source["passed"];
	        this.failed = source["failed"];
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

}

export namespace main {
	
	export class RelayRuleInfo {
	    name: string;
	    proto: string;
	    localIp: string;
	    localPort: number;
	    remotePort: number;
	    source: string;
	    target: string;
	
	    static createFrom(source: any = {}) {
	        return new RelayRuleInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.proto = source["proto"];
	        this.localIp = source["localIp"];
	        this.localPort = source["localPort"];
	        this.remotePort = source["remotePort"];
	        this.source = source["source"];
	        this.target = source["target"];
	    }
	}
	export class RelayInfo {
	    server: string;
	    hasToken: boolean;
	    running: boolean;
	    pid: number;
	    rules: RelayRuleInfo[];
	
	    static createFrom(source: any = {}) {
	        return new RelayInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.server = source["server"];
	        this.hasToken = source["hasToken"];
	        this.running = source["running"];
	        this.pid = source["pid"];
	        this.rules = this.convertValues(source["rules"], RelayRuleInfo);
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
	
	export class RouteInfo {
	    name: string;
	    hostname: string;
	    service: string;
	    authUser: string;
	
	    static createFrom(source: any = {}) {
	        return new RouteInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.hostname = source["hostname"];
	        this.service = source["service"];
	        this.authUser = source["authUser"];
	    }
	}
	export class StateInfo {
	    hasAuth: boolean;
	    tunnelId: string;
	    tunnelName: string;
	    tunnelRunning: boolean;
	    tunnelPid: number;
	    relayServer: string;
	    relayRunning: boolean;
	    relayPid: number;
	    routeCount: number;
	    ruleCount: number;
	    authCount: number;
	    fastRunning: boolean;
	    dataDir: string;
	    portable: boolean;
	
	    static createFrom(source: any = {}) {
	        return new StateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hasAuth = source["hasAuth"];
	        this.tunnelId = source["tunnelId"];
	        this.tunnelName = source["tunnelName"];
	        this.tunnelRunning = source["tunnelRunning"];
	        this.tunnelPid = source["tunnelPid"];
	        this.relayServer = source["relayServer"];
	        this.relayRunning = source["relayRunning"];
	        this.relayPid = source["relayPid"];
	        this.routeCount = source["routeCount"];
	        this.ruleCount = source["ruleCount"];
	        this.authCount = source["authCount"];
	        this.fastRunning = source["fastRunning"];
	        this.dataDir = source["dataDir"];
	        this.portable = source["portable"];
	    }
	}
	export class TunnelInfo {
	    id: string;
	    name: string;
	    inUse: boolean;
	
	    static createFrom(source: any = {}) {
	        return new TunnelInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.inUse = source["inUse"];
	    }
	}

}

export namespace relay {
	
	export class RuleCheckResult {
	    name: string;
	    proto: string;
	    local_port: number;
	    remote_port: number;
	    local_ok: boolean;
	    remote_ok: boolean;
	    latency_ms: number;
	    local_err?: string;
	    remote_err?: string;
	    skipped?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RuleCheckResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.proto = source["proto"];
	        this.local_port = source["local_port"];
	        this.remote_port = source["remote_port"];
	        this.local_ok = source["local_ok"];
	        this.remote_ok = source["remote_ok"];
	        this.latency_ms = source["latency_ms"];
	        this.local_err = source["local_err"];
	        this.remote_err = source["remote_err"];
	        this.skipped = source["skipped"];
	    }
	}
	export class CheckResult {
	    server: string;
	    server_ok: boolean;
	    server_latency_ms: number;
	    frpc_running: boolean;
	    frpc_pid: number;
	    rules: RuleCheckResult[];
	    total: number;
	    passed: number;
	    failed: number;
	    skipped?: number;
	
	    static createFrom(source: any = {}) {
	        return new CheckResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.server = source["server"];
	        this.server_ok = source["server_ok"];
	        this.server_latency_ms = source["server_latency_ms"];
	        this.frpc_running = source["frpc_running"];
	        this.frpc_pid = source["frpc_pid"];
	        this.rules = this.convertValues(source["rules"], RuleCheckResult);
	        this.total = source["total"];
	        this.passed = source["passed"];
	        this.failed = source["failed"];
	        this.skipped = source["skipped"];
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

}

