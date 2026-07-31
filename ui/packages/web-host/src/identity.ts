/**
 * Canonical GitHub repository identity ("owner/repo", lower-cased).
 *
 * This is a TypeScript port of factory-core's `canonical_github_identity`
 * (factory-core/src/config.rs). The normalization rules are copied verbatim so
 * the ui registry and core agree on a single canonical identity for any given
 * git remote. Keep the two implementations in lockstep: if one changes, the
 * other must change too.
 *
 * Supported remote syntaxes:
 *   - git@github.com:owner/repo[.git]
 *   - https://[user@]github.com[:port]/owner/repo[.git]
 *   - ssh://git@github.com[:port]/owner/repo[.git]  (also ssh.github.com)
 *
 * Trailing slashes and a single `.git` suffix are stripped. Owner and repo are
 * lower-cased. Anything else (extra path segments, missing owner/repo,
 * unsupported host or scheme) throws a `NormalizationError`.
 */

export class NormalizationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "NormalizationError";
  }
}

export function canonicalGithubIdentity(origin: string): string {
  const trimmed = origin.trim();

  // Already-canonical "owner/repo" (no scheme, no host, exactly two non-empty
  // segments) is accepted as-is. This lets the registry's own stored identity
  // round-trip through the same function used for raw remotes.
  if (!trimmed.includes(":") && !trimmed.includes("@")) {
    const segments = trimmed.split("/").filter((segment) => segment.length > 0);
    if (segments.length === 2) {
      const [owner, repository] = segments;
      return `${owner!.toLowerCase()}/${repository!.toLowerCase()}`;
    }
  }

  let path: string;

  const scpLike = stripPrefix(trimmed, "git@github.com:");
  const https = stripPrefix(trimmed, "https://");
  const ssh = stripPrefix(trimmed, "ssh://git@");

  if (scpLike !== null) {
    path = scpLike;
  } else if (https !== null) {
    const slash = https.indexOf("/");
    if (slash === -1) {
      throw new NormalizationError("GitHub HTTPS origin has no repository path");
    }
    const authority = https.slice(0, slash);
    path = https.slice(slash + 1);
    // Strip optional userinfo ("user@host") and port ("host:port").
    const afterAt = authority.includes("@")
      ? authority.slice(authority.lastIndexOf("@") + 1)
      : authority;
    const host = afterAt.split(":")[0] ?? afterAt;
    if (host.toLowerCase() !== "github.com") {
      throw new NormalizationError("GitHub HTTPS origin has an unsupported host");
    }
  } else if (ssh !== null) {
    const slash = ssh.indexOf("/");
    if (slash === -1) {
      throw new NormalizationError("GitHub SSH origin has no repository path");
    }
    const authority = ssh.slice(0, slash);
    path = ssh.slice(slash + 1);
    const host = authority.split(":")[0] ?? authority;
    const lowered = host.toLowerCase();
    if (lowered !== "github.com" && lowered !== "ssh.github.com") {
      throw new NormalizationError("GitHub SSH origin has an unsupported host");
    }
  } else {
    throw new NormalizationError("unsupported GitHub origin syntax");
  }

  path = stripTrailingSlashes(path);
  if (path.endsWith(".git")) {
    path = path.slice(0, -".git".length);
  }

  const segments = path.split("/");
  const owner = segments[0];
  const repository = segments[1];
  if (!owner) {
    throw new NormalizationError("GitHub origin has no owner");
  }
  if (!repository) {
    throw new NormalizationError("GitHub origin has no repository");
  }
  if (segments.length > 2) {
    throw new NormalizationError("GitHub origin has an invalid repository path");
  }

  return `${owner.toLowerCase()}/${repository.toLowerCase()}`;
}

function stripPrefix(value: string, prefix: string): string | null {
  return value.startsWith(prefix) ? value.slice(prefix.length) : null;
}

function stripTrailingSlashes(value: string): string {
  let end = value.length;
  while (end > 0 && value.charCodeAt(end - 1) === 47 /* '/' */) {
    end -= 1;
  }
  return value.slice(0, end);
}
