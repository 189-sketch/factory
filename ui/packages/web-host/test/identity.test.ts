import { describe, expect, it } from "vitest";
import { canonicalGithubIdentity, NormalizationError } from "../src/identity.js";

// Mirrors factory-core/src/config.rs `canonical_github_identity` semantics.
// These cases are the contract: the ui registry and core must agree byte-for-byte
// on the canonical identity for any remote a user can paste.
describe("canonicalGithubIdentity", () => {
  it("normalizes scp-like git@ syntax", () => {
    expect(canonicalGithubIdentity("git@github.com:Owner/Repo.git")).toBe("owner/repo");
  });

  it("normalizes scp-like without .git suffix", () => {
    expect(canonicalGithubIdentity("git@github.com:Owner/Repo")).toBe("owner/repo");
  });

  it("normalizes https with .git and trailing slash", () => {
    expect(canonicalGithubIdentity("https://github.com/Owner/Repo.git/")).toBe("owner/repo");
  });

  it("normalizes https with userinfo", () => {
    expect(canonicalGithubIdentity("https://user@github.com/Owner/Repo")).toBe("owner/repo");
  });

  it("normalizes https with port", () => {
    expect(canonicalGithubIdentity("https://github.com:443/Owner/Repo")).toBe("owner/repo");
  });

  it("normalizes ssh:// git@ form", () => {
    expect(canonicalGithubIdentity("ssh://git@github.com/Owner/Repo")).toBe("owner/repo");
  });

  it("normalizes ssh:// with port", () => {
    expect(canonicalGithubIdentity("ssh://git@github.com:22/Owner/Repo.git")).toBe(
      "owner/repo",
    );
  });

  it("accepts ssh.github.com host", () => {
    expect(canonicalGithubIdentity("ssh://git@ssh.github.com/Owner/Repo")).toBe(
      "owner/repo",
    );
  });

  it("lower-cases owner and repo", () => {
    expect(canonicalGithubIdentity("git@github.com:AbC/XyZ.git")).toBe("abc/xyz");
  });

  it("trims surrounding whitespace", () => {
    expect(canonicalGithubIdentity("  git@github.com:Owner/Repo.git  ")).toBe("owner/repo");
  });

  it("rejects unsupported https host", () => {
    expect(() => canonicalGithubIdentity("https://example.com/Owner/Repo")).toThrow(
      NormalizationError,
    );
  });

  it("rejects unsupported ssh host", () => {
    expect(() => canonicalGithubIdentity("ssh://git@example.com/Owner/Repo")).toThrow(
      NormalizationError,
    );
  });

  it("rejects unsupported scheme", () => {
    expect(() => canonicalGithubIdentity("ftp://github.com/Owner/Repo")).toThrow(
      NormalizationError,
    );
  });

  it("rejects extra path segments", () => {
    expect(() => canonicalGithubIdentity("git@github.com:Owner/Repo/Extra")).toThrow(
      NormalizationError,
    );
  });

  it("rejects missing repo", () => {
    expect(() => canonicalGithubIdentity("git@github.com:Owner")).toThrow(
      NormalizationError,
    );
  });

  it("rejects https with no path", () => {
    expect(() => canonicalGithubIdentity("https://github.com")).toThrow(NormalizationError);
  });

  it("produces identical identity for equivalent remotes", () => {
    const a = canonicalGithubIdentity("git@github.com:Owner/Repo.git");
    const b = canonicalGithubIdentity("https://github.com/owner/repo");
    const c = canonicalGithubIdentity("ssh://git@github.com/OWNER/REPO");
    expect(a).toBe(b);
    expect(b).toBe(c);
  });
});
