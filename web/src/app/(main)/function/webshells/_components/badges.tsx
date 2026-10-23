import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export function WebShellStatusBadge({ status, className }: { status: string; className?: string }) {
  if (status === "ok") {
    return (
      <Badge
        className={cn(
          "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
          className,
        )}
        variant="outline"
      >
        连接正常
      </Badge>
    );
  }
  if (status === "fail") {
    return (
      <Badge className={cn("border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-400", className)} variant="outline">
        连接失败
      </Badge>
    );
  }
  return (
    <Badge className={cn("text-muted-foreground", className)} variant="outline">
      未测试
    </Badge>
  );
}
