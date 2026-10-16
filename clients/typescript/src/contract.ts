export interface paths {
    "/v1/workers/pair": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["pairWorker"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/admin/v1/credentials": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["adminCredentials"];
        put?: never;
        post: operations["adminIssueCredential"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/admin/v1/credentials/{id}/rotate": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["adminRotateCredential"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/admin/v1/credentials/{id}/revoke": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["adminRevokeCredential"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/admin/v1/pairing": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["adminCreatePairing"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/admin/v1/session": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["adminSession"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/admin/v1/login": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["adminLogin"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/admin/v1/logout": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["adminLogout"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/admin/v1/summary": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["adminSummary"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/admin/v1/api": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["adminApi"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/admin/v1/workers": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["adminWorkers"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/admin/v1/queue": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["adminQueue"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/workers/hello": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["workerHello"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/workers/goodbye": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["workerGoodbye"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/workers/me": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["workerWhoAmI"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/jobs/next": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["claimJob"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/jobs/{id}/ack": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["ackJob"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/jobs/{id}/result": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["submitResult"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/jobs": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["submitJob"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/jobs/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["getJob"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/jobs/{id}/cancel": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["cancelJob"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/status": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["getStatus"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export type webhooks = Record<string, never>;
export interface components {
    schemas: {
        Credential: {
            credential_id: string;
            principal_id: string;
            role: "caller" | "worker";
            created_at: string;
            expires_at: string;
            revoked: boolean;
        };
        CredentialPage: {
            total: number;
            offset: number;
            limit: number;
            items: components["schemas"]["Credential"][];
        };
        IssueCredentialRequest: {
            role: "caller" | "worker";
            ttl_seconds: number;
        };
        RotateCredentialRequest: {
            ttl_seconds: number;
        };
        IssuedCredential: {
            credential: components["schemas"]["Credential"];
            token: string;
        };
        CreatePairingRequest: {
            worker_id?: string;
            ttl_seconds: number;
            credential_ttl_seconds: number;
        };
        PairingCode: {
            code: string;
            expires_at: string;
            worker_id: string;
        };
        PairRequest: {
            code: string;
        };
        AdminSession: {
            authenticated: boolean;
            role?: "admin-read" | "admin-manage";
            credential_management?: boolean;
            public_url?: string;
        };
        AdminLogin: {
            password: string;
        };
        Error: {
            error: string;
        };
        UpgradeError: {
            error: string;
            min_worker_version: string;
        };
        HelloRequest: {
            worker_version: string;
            worker_name?: string;
            protocol_version: number;
            models: string[];
            capacity: number;
            features?: string[];
        };
        HelloResponse: {
            worker_id: string;
            min_worker_version: string;
            max_wait: string;
            recommended_worker_version?: string;
        };
        WhoAmI: {
            worker_id: string;
            min_worker_version: string;
            recommended_worker_version?: string;
        };
        SubmitRequest: {
            model: string;
            prompt: string;
            system?: string;
            format?: "" | "json";
            think?: boolean;
            num_predict: number;
            deadline_ms: number;
        };
        Assignment: {
            job_id: string;
            lease_id: string;
            lease_expires_at: string;
            model: string;
            prompt: string;
            system?: string;
            format?: string;
            think?: boolean;
            num_predict: number;
        };
        AckRequest: {
            lease_id: string;
        };
        ResultRequest: {
            lease_id: string;
            output?: string;
            thinking?: string;
            done_reason?: string;
            error?: string;
        };
        JobStatus: {
            job_id: string;
            state: "queued" | "leased" | "done" | "failed" | "expired" | "cancelled";
            output?: string;
            thinking?: string;
            done_reason?: string;
            error?: string;
        };
        Status: {
            workers_online: number;
            models: string[];
        };
        AdminApiCounters: {
            total_requests: number;
            window_requests: number;
            inflight: number;
            success: number;
            redirects: number;
            client_errors: number;
            server_errors: number;
            unauthorized: number;
            rate_limited: number;
            mean_latency_ms: number | null;
        };
        AdminApiRoute: {
            total_requests: number;
            window_requests: number;
            inflight: number;
            success: number;
            redirects: number;
            client_errors: number;
            server_errors: number;
            unauthorized: number;
            rate_limited: number;
            mean_latency_ms: number | null;
            route: string;
        };
        AdminApiBucket: {
            at: string;
            requests: number;
            client_errors: number;
            server_errors: number;
            rate_limited: number;
        };
        AdminApi: {
            total_requests: number;
            window_requests: number;
            inflight: number;
            success: number;
            redirects: number;
            client_errors: number;
            server_errors: number;
            unauthorized: number;
            rate_limited: number;
            mean_latency_ms: number | null;
            snapshot_at: string;
            epoch: string;
            uptime_seconds: number;
            status: "serving";
            window_seconds: 300;
            requests_per_second: number;
            routes: components["schemas"]["AdminApiRoute"][];
            history: components["schemas"]["AdminApiBucket"][];
        };
        AdminSummary: {
            snapshot_at: string;
            epoch: string;
            workers_online: number;
            capacity: number;
            active_slots: number;
            free_slots: number;
            queued: number;
            leased: number;
            done: number;
            failed: number;
            expired: number;
            cancelled: number;
            models: string[];
            oldest_queued_ms: number;
            latency_samples: number;
            mean_wait_ms: number | null;
            mean_completion_ms: number | null;
            measurement_window_seconds: 300;
        };
        AdminWorker: {
            worker_id: string;
            worker_name?: string;
            worker_version: string;
            models: string[];
            capacity: number;
            live_leases: number;
            free_slots: number;
            last_seen: string;
            state: "online-idle" | "online-busy" | "offline";
        };
        AdminJob: {
            job_id: string;
            model: string;
            state: "queued" | "leased" | "done" | "failed" | "expired" | "cancelled";
            created_at: string;
            deadline_at: string;
            leased_at?: string;
            worker_id?: string;
            wait_ms?: number;
        };
        AdminWorkers: {
            snapshot_at: string;
            epoch: string;
            total: number;
            offset: number;
            limit: number;
            items: components["schemas"]["AdminWorker"][];
        };
        AdminQueue: {
            snapshot_at: string;
            epoch: string;
            total: number;
            offset: number;
            limit: number;
            items: components["schemas"]["AdminJob"][];
        };
    };
    responses: {
        BadRequest: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        Unauthorized: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        QueueUnavailable: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        TooManyRequests: {
            headers: {
                "Retry-After"?: number;
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        UpgradeRequired: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["UpgradeError"];
            };
        };
    };
    parameters: {
        IdempotencyKey: string;
        AdminModel: string;
        AdminWorkerState: "online-idle" | "online-busy" | "offline";
        AdminJobState: "queued" | "leased" | "done" | "failed" | "expired" | "cancelled";
        AdminOffset: number;
        AdminLimit: number;
        WorkerVersion: string;
        RequestID: string;
        JobID: string;
    };
    requestBodies: never;
    headers: never;
    pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
    pairWorker: {
        parameters: {
            query?: never;
            header: {
                "X-DUMB-Worker-Version": components["parameters"]["WorkerVersion"];
            };
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["PairRequest"];
            };
        };
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["IssuedCredential"];
                };
            };
            400: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            415: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            426: components["responses"]["UpgradeRequired"];
            429: components["responses"]["TooManyRequests"];
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
        };
    };
    adminCredentials: {
        parameters: {
            query?: {
                offset?: number;
                limit?: number;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["CredentialPage"];
                };
            };
            400: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            429: components["responses"]["TooManyRequests"];
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
        };
    };
    adminIssueCredential: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["IssueCredentialRequest"];
            };
        };
        responses: {
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["IssuedCredential"];
                };
            };
            400: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            415: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            429: components["responses"]["TooManyRequests"];
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
        };
    };
    adminRotateCredential: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["RotateCredentialRequest"];
            };
        };
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["IssuedCredential"];
                };
            };
            400: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            415: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            429: components["responses"]["TooManyRequests"];
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
        };
    };
    adminRevokeCredential: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": Record<string, never>;
            };
        };
        responses: {
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            415: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            429: components["responses"]["TooManyRequests"];
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
        };
    };
    adminCreatePairing: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CreatePairingRequest"];
            };
        };
        responses: {
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PairingCode"];
                };
            };
            400: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            415: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            429: components["responses"]["TooManyRequests"];
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
        };
    };
    adminSession: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["AdminSession"];
                };
            };
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            429: components["responses"]["TooManyRequests"];
        };
    };
    adminLogin: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["AdminLogin"];
            };
        };
        responses: {
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            415: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            429: components["responses"]["TooManyRequests"];
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
        };
    };
    adminLogout: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
        };
    };
    adminSummary: {
        parameters: {
            query?: {
                model?: components["parameters"]["AdminModel"];
            };
            header?: {
                "X-Request-ID"?: components["parameters"]["RequestID"];
            };
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["AdminSummary"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
        };
    };
    adminApi: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["AdminApi"];
                };
            };
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
        };
    };
    adminWorkers: {
        parameters: {
            query?: {
                model?: components["parameters"]["AdminModel"];
                state?: components["parameters"]["AdminWorkerState"];
                offset?: components["parameters"]["AdminOffset"];
                limit?: components["parameters"]["AdminLimit"];
            };
            header?: {
                "X-Request-ID"?: components["parameters"]["RequestID"];
            };
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["AdminWorkers"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
        };
    };
    adminQueue: {
        parameters: {
            query?: {
                model?: components["parameters"]["AdminModel"];
                state?: components["parameters"]["AdminJobState"];
                offset?: components["parameters"]["AdminOffset"];
                limit?: components["parameters"]["AdminLimit"];
            };
            header?: {
                "X-Request-ID"?: components["parameters"]["RequestID"];
            };
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["AdminQueue"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
        };
    };
    workerHello: {
        parameters: {
            query?: never;
            header: {
                "X-DUMB-Worker-Version": components["parameters"]["WorkerVersion"];
                "X-Request-ID"?: components["parameters"]["RequestID"];
            };
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["HelloRequest"];
            };
        };
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["HelloResponse"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            426: components["responses"]["UpgradeRequired"];
            429: components["responses"]["TooManyRequests"];
        };
    };
    workerGoodbye: {
        parameters: {
            query?: never;
            header: {
                "X-DUMB-Worker-Version": components["parameters"]["WorkerVersion"];
                "X-Request-ID"?: components["parameters"]["RequestID"];
            };
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            426: components["responses"]["UpgradeRequired"];
            429: components["responses"]["TooManyRequests"];
        };
    };
    workerWhoAmI: {
        parameters: {
            query?: never;
            header: {
                "X-DUMB-Worker-Version": components["parameters"]["WorkerVersion"];
                "X-Request-ID"?: components["parameters"]["RequestID"];
            };
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["WhoAmI"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            426: components["responses"]["UpgradeRequired"];
            429: components["responses"]["TooManyRequests"];
        };
    };
    claimJob: {
        parameters: {
            query?: {
                wait?: string;
            };
            header: {
                "X-DUMB-Worker-Version": components["parameters"]["WorkerVersion"];
                "X-Request-ID"?: components["parameters"]["RequestID"];
            };
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Assignment"];
                };
            };
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            426: components["responses"]["UpgradeRequired"];
            429: components["responses"]["TooManyRequests"];
            503: components["responses"]["QueueUnavailable"];
        };
    };
    ackJob: {
        parameters: {
            query?: never;
            header: {
                "X-DUMB-Worker-Version": components["parameters"]["WorkerVersion"];
                "X-Request-ID"?: components["parameters"]["RequestID"];
            };
            path: {
                id: components["parameters"]["JobID"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["AckRequest"];
            };
        };
        responses: {
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            426: components["responses"]["UpgradeRequired"];
            429: components["responses"]["TooManyRequests"];
            503: components["responses"]["QueueUnavailable"];
        };
    };
    submitResult: {
        parameters: {
            query?: never;
            header: {
                "X-DUMB-Worker-Version": components["parameters"]["WorkerVersion"];
                "X-Request-ID"?: components["parameters"]["RequestID"];
            };
            path: {
                id: components["parameters"]["JobID"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ResultRequest"];
            };
        };
        responses: {
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            426: components["responses"]["UpgradeRequired"];
            429: components["responses"]["TooManyRequests"];
            503: components["responses"]["QueueUnavailable"];
        };
    };
    submitJob: {
        parameters: {
            query?: {
                wait?: string;
            };
            header?: {
                "X-Request-ID"?: components["parameters"]["RequestID"];
                "Idempotency-Key"?: components["parameters"]["IdempotencyKey"];
            };
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SubmitRequest"];
            };
        };
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["JobStatus"];
                };
            };
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["JobStatus"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            410: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["JobStatus"];
                };
            };
            429: components["responses"]["TooManyRequests"];
            503: {
                headers: {
                    "Retry-After"?: number;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
        };
    };
    getJob: {
        parameters: {
            query?: {
                wait?: string;
            };
            header?: {
                "X-Request-ID"?: components["parameters"]["RequestID"];
            };
            path: {
                id: components["parameters"]["JobID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["JobStatus"];
                };
            };
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["JobStatus"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            410: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["JobStatus"];
                };
            };
            429: components["responses"]["TooManyRequests"];
            503: components["responses"]["QueueUnavailable"];
        };
    };
    cancelJob: {
        parameters: {
            query?: never;
            header?: {
                "X-Request-ID"?: components["parameters"]["RequestID"];
            };
            path: {
                id: components["parameters"]["JobID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["JobStatus"];
                };
            };
            401: components["responses"]["Unauthorized"];
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["JobStatus"];
                };
            };
            429: components["responses"]["TooManyRequests"];
            503: components["responses"]["QueueUnavailable"];
        };
    };
    getStatus: {
        parameters: {
            query?: never;
            header?: {
                "X-Request-ID"?: components["parameters"]["RequestID"];
            };
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Status"];
                };
            };
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
        };
    };
}

export const limits = {"maxDeadlineMs":600000,"maxNumPredict":128,"defaultNumPredict":128,"maxIdempotencyKeyLength":64,"idempotencyKeyPattern":"^[A-Za-z0-9._:-]{1,64}$"} as const;
