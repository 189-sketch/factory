import React from "react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
const zeroTime = "0001-01-01T00:00:00Z";

export function State({ value, compact = false }) {
  const tones = {
    running: "border-primary/25 bg-primary/10 text-primary",
    queued: "border-border bg-muted text-muted-foreground",
    blocked: "border-warning/25 bg-warning/10 text-warning",
    awaiting_approval: "border-warning/25 bg-warning/10 text-warning",
    awaiting_merge: "border-info/25 bg-info/10 text-info",
    interrupted: "border-warning/25 bg-warning/10 text-warning",
    succeeded: "border-success/25 bg-success/10 text-success",
    failed: "border-danger/25 bg-danger/10 text-danger",
    timed_out: "border-danger/25 bg-danger/10 text-danger",
    cancelled: "border-danger/25 bg-danger/10 text-danger",
  };
  return (
    <Badge className={cn("gap-1.5", tones[value] || tones.queued)}>
      <span className="size-1.5 rounded-full bg-current" />
      {compact ? ({awaiting_approval:"Approval",awaiting_merge:"PR ready"}[value] || stateLabel(value)) : stateLabel(value)}
    </Badge>
  );
}

export function friendlyName(name) {
  return String(name || "")
    .replaceAll("_", " ")
    .replaceAll("-", " ")
    .replace(/^./, (c) => c.toUpperCase());
}
export function relativeTime(value) {
  if (!value || value === zeroTime) return "Not started";
  const seconds = Math.max(
    0,
    Math.floor((Date.now() - Date.parse(value)) / 1000),
  );
  if (seconds < 10) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}
export function formatTimestamp(value) {
  return !value || value === zeroTime || !Number.isFinite(Date.parse(value))
    ? "Unavailable"
    : new Date(value).toLocaleString();
}
export function stateLabel(value) {
  return friendlyName(value || "unknown");
}
