"use client";

import * as React from "react";

import {
  Loader2Icon,
  PencilIcon,
  PlusIcon,
  PlugZapIcon,
  RefreshCwIcon,
  SearchIcon,
  TerminalIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { WebShell } from "@/lib/types";
import { cn } from "@/lib/utils";

import { WebShellStatusBadge } from "./_components/badges";
import { WebShellConnectSheet } from "./_components/connect-sheet";
import { WebShellFormDialog } from "./_components/form-dialog";
import { fmtTime, osLabel, shellAddress, typeLabel } from "./_components/shared";

const POLL_MS = 10_000;

export default function WebShellsPage() {
  const [items, setItems] = React.useState<WebShell[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [query, setQuery] = React.useState("");
  const [queryQ, setQueryQ] = React.useState("");
  const [status, setStatus] = React.useState("all");
  const [testingId, setTestingId] = React.useState<number | null>(null);

  const [formOpen, setFormOpen] = React.useState(false);
  const [editId, setEditId] = React.useState<number | null>(null);
  const [connectShell, setConnectShell] = React.useState<WebShell | null>(null);
  const [connectOpen, setConnectOpen] = React.useState(false);

  React.useEffect(() => {
    const t = setTimeout(() => setQueryQ(query.trim()), 300);
    return () => clearTimeout(t);
  }, [query]);

  // silent=true is used by the poller: don't flash a spinner on every tick.
  const load = React.useCallback(
    async (silent = false) => {
      if (!silent) setLoading(true);
      try {
        const r = await api.webShells({ q: queryQ || undefined, status });
        setItems(r.items);
      } catch (e) {
        if (!silent) toast.error(`读取 WebShell 列表失败：${(e as Error).message}`);
      } finally {
        if (!silent) setLoading(false);
      }
    },
    [queryQ, status],
  );

  React.useEffect(() => {
    load();
  }, [load]);

  React.useEffect(() => {
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") load(true);
    }, POLL_MS);
    return () => clearInterval(timer);
  }, [load]);

  const openCreate = () => {
    setEditId(null);
    setFormOpen(true);
  };

  const openEdit = (shell: WebShell) => {
    setEditId(shell.id);
    setFormOpen(true);
  };

  const openConnect = (shell: WebShell) => {
    setConnectShell(shell);
    setConnectOpen(true);
  };

  const test = async (shell: WebShell) => {
    setTestingId(shell.id);
    try {
      const r = await api.testWebShell(shell.id);
      if (r.ok) {
        toast.success(
          `${shell.name} 连接成功${r.os_type ? `（${r.os_type === "windows" ? "Windows" : "Linux"}）` : ""}，耗时 ${r.duration_ms} ms`,
        );
      } else {
        toast.error(`${shell.name} 连接失败：${r.error || "目标未执行测试命令"}`);
      }
      load(true);
    } catch (e) {
      toast.error(`测试失败：${(e as Error).message}`);
    } finally {
      setTestingId(null);
    }
  };

  const remove = async (shell: WebShell) => {
    if (!window.confirm(`确认删除 WebShell「${shell.name}」？该操作只删除登记信息，不会触碰目标服务器上的文件。`)) return;
    try {
      await api.deleteWebShell(shell.id);
      toast.success("已删除");
      setItems((cur) => cur.filter((s) => s.id !== shell.id));
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  };

  const okCount = items.filter((s) => s.status === "ok").length;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">WebShell</h1>
          <p className="text-muted-foreground text-sm">
            Agent 拿到目标 shell 后会自动登记，也可以手动添加；连接后可执行命令、管理文件、查看主机信息。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative">
            <SearchIcon className="text-muted-foreground absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="搜索名称 / URL / 目标"
              className="h-8 w-52 pl-8 text-sm"
            />
          </div>
          <Select value={status} onValueChange={setStatus}>
            <SelectTrigger className="h-8 w-28" size="sm">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部状态</SelectItem>
              <SelectItem value="ok">连接正常</SelectItem>
              <SelectItem value="fail">连接失败</SelectItem>
              <SelectItem value="unknown">未测试</SelectItem>
            </SelectContent>
          </Select>
          <Button variant="ghost" size="icon" className="size-8" onClick={() => load()} title="刷新">
            <RefreshCwIcon className={cn("size-4", loading && "animate-spin")} />
          </Button>
          <Button size="sm" onClick={openCreate}>
            <PlusIcon /> 手动添加
          </Button>
        </div>
      </div>

      <div className="text-muted-foreground flex items-center gap-3 text-xs">
        <span>共 {items.length} 个</span>
        <span className="text-emerald-600 dark:text-emerald-400">正常 {okCount}</span>
        {items.length > okCount && <span>其余 {items.length - okCount} 个待验证</span>}
      </div>

      <Card className="overflow-hidden py-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>名称</TableHead>
              <TableHead className="w-32">类型</TableHead>
              <TableHead>目标</TableHead>
              <TableHead className="w-24">状态</TableHead>
              <TableHead className="w-24">系统</TableHead>
              <TableHead className="w-36">关联任务</TableHead>
              <TableHead className="w-32">最近使用</TableHead>
              <TableHead className="w-44 text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading && items.length === 0 && (
              <TableRow>
                <TableCell colSpan={8} className="text-muted-foreground py-12 text-center text-sm">
                  <Loader2Icon className="mr-2 inline size-4 animate-spin" /> 加载中…
                </TableCell>
              </TableRow>
            )}
            {!loading && items.length === 0 && (
              <TableRow>
                <TableCell colSpan={8} className="py-12 text-center">
                  <div className="text-muted-foreground flex flex-col items-center gap-2 text-sm">
                    <TerminalIcon className="size-8 opacity-40" />
                    <p>还没有登记任何 WebShell。</p>
                    <p className="text-xs">
                      Agent 拿到 shell 后会自动出现在这里；也可以点右上角「手动添加」。
                    </p>
                  </div>
                </TableCell>
              </TableRow>
            )}
            {items.map((shell) => (
              <TableRow key={shell.id} className="group">
                <TableCell>
                  <button type="button" onClick={() => openConnect(shell)} className="text-left hover:underline">
                    <div className="flex items-center gap-2 font-medium">
                      <TerminalIcon className="text-muted-foreground size-3.5 shrink-0" />
                      <span className="max-w-[220px] truncate">{shell.name || `#${shell.id}`}</span>
                      {shell.created_by === "agent" && (
                        <Badge variant="secondary" className="px-1.5 py-0 text-[10px] font-normal">
                          Agent
                        </Badge>
                      )}
                    </div>
                    <div className="text-muted-foreground mt-0.5 max-w-[320px] truncate font-mono text-xs">
                      {shell.url}
                    </div>
                  </button>
                </TableCell>
                <TableCell>
                  <span className="text-sm">{typeLabel(shell.type)}</span>
                  <div className="text-muted-foreground text-xs uppercase">{shell.lang}</div>
                </TableCell>
                <TableCell>
                  <span className="max-w-[200px] truncate font-mono text-xs" title={shellAddress(shell)}>
                    {shellAddress(shell) || "—"}
                  </span>
                  {shell.last_error && (
                    <div className="max-w-[220px] truncate text-red-500 text-xs" title={shell.last_error}>
                      {shell.last_error}
                    </div>
                  )}
                </TableCell>
                <TableCell>
                  <WebShellStatusBadge status={shell.status} />
                </TableCell>
                <TableCell className="text-sm">{osLabel(shell.os_type)}</TableCell>
                <TableCell className="text-muted-foreground text-xs">
                  {shell.task_name || (shell.task_id ? `#${shell.task_id}` : "全局")}
                </TableCell>
                <TableCell className="text-muted-foreground text-xs">{fmtTime(shell.last_used_at)}</TableCell>
                <TableCell>
                  <div className="flex items-center justify-end gap-1">
                    <Button variant="outline" size="sm" className="h-7 px-2" onClick={() => openConnect(shell)}>
                      <TerminalIcon className="size-3.5" /> 连接
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-7"
                      title="测试连接"
                      disabled={testingId === shell.id}
                      onClick={() => test(shell)}
                    >
                      {testingId === shell.id ? (
                        <Loader2Icon className="size-3.5 animate-spin" />
                      ) : (
                        <PlugZapIcon className="size-3.5" />
                      )}
                    </Button>
                    <Button variant="ghost" size="icon" className="size-7" title="编辑" onClick={() => openEdit(shell)}>
                      <PencilIcon className="size-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="text-destructive size-7"
                      title="删除"
                      onClick={() => remove(shell)}
                    >
                      <Trash2Icon className="size-3.5" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>

      <WebShellFormDialog
        open={formOpen}
        onOpenChange={setFormOpen}
        editId={editId}
        onSaved={() => load(true)}
      />

      <WebShellConnectSheet
        shell={connectShell}
        open={connectOpen}
        onOpenChange={setConnectOpen}
        onChanged={() => load(true)}
      />
    </div>
  );
}
