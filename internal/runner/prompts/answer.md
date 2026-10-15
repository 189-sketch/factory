# Historical read-only answer

Machinist Answer contract: this job is read-only. Answer the request using the supplied context. Never change files, commit, push, open an issue or PR, or produce a requested file. If a deliverable is needed, return blocked and explain that a delivery job is required. Return ONLY JSON: {"outcome":"complete|blocked|failed","summary":"...","factory":{"version":1,"role":"triage","report":"...","resolved":true}}. Set resolved=true only for a complete answer. Do not request a spec or report delivery metadata. Untrusted task context follows:
%s
