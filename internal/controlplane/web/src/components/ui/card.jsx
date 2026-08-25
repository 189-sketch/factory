import { cn } from "@/lib/utils";

export function Card({ className, ...props }) {
  return <div className={cn("rounded-lg border border-border bg-surface shadow-xs", className)} {...props} />;
}
