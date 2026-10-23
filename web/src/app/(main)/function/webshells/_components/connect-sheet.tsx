"use client";

import * as React from "react";

import {
  ArrowUpIcon,
  DownloadIcon,
  EraserIcon,
  FileIcon,
  FolderIcon,
  FolderPlusIcon,
  InfoIcon,
  Loader2Icon,
  PencilIcon,
  PlayIcon,
  PlugZapIcon,
  RefreshCwIcon,
  SaveIcon,
  TerminalIcon,
  Trash2Icon,
  UploadIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type {
  WebShell,
  WebShellFileData,
  WebShellFileEntry,
  WebShellFileListing,
  WebShellInfo,
} from "@/lib/types";
import { cn } from "@/lib/utils";

import { WebShellStatusBadge } from "./badges";
import {
  b64ToBytes,
  bytesToB64,
  decodeTextBytes,
  downloadBytes,
  fmtSize,
  fmtTime,
  joinRemotePath,
  osLabel,
  typeLabel,
} from "./shared";

const MAX_UPLOAD = 8 * 1024 * 1024;
const EDIT_READ_BYTES = 1024 * 1024;
const DOWNLOAD_READ_BYTES = 4 * 1024 * 1024;

export function WebShellConnectSheet({
  shell,
  open,
  onOpenChange,
  onChanged,
}: {
  shell: WebShell | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onChanged: () => void;
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 sm:max-w-5xl">
        {shell && <ConnectBody key={shell.id} shell={shell} onChanged={onChanged} />}
      </SheetContent>
    </Sheet>
  );
}

function ConnectBody({ shell, onChanged }: { shell: WebShell; onChanged: () => void }) {
  const [testing, setTesting] = React.useState(false);

  const test = async () => {
    setTesting(true);
    try {
      const r = await api.testWebShell(shell.id);
      if (r.ok) {
        toast.success(`连接正常${r.os_type ? `（${osLabel(r.os_type)}）` : ""}${r.transport ? ` · ${r.transport}` : ""}`);
      } else {
        toast.error(r.error || "目标有响应，但未执行测试命令");
      }
      onChanged();
    } catch (e) {
      toast.error(`测试失败：${(e as Error).message}`);
    } finally {
      setTesting(false);
    }
  };

  return (
    <>
      <SheetHeader className="border-b p-4 pr-14">
        <div className="flex flex-wrap items-center gap-2">
          <SheetTitle className="max-w-[60%] truncate text-base">{shell.name || shell.url}</SheetTitle>
          <Badge variant="outline">{typeLabel(shell.type)}</Badge>
          <WebShellStatusBadge status={shell.status} />
          {shell.os_type && <Badge variant="secondary">{osLabel(shell.os_type)}</Badge>}
          {shell.transport && (
            <Badge variant="outline" className="font-mono text-[10px]">
              {shell.transport}
            </Badge>
          )}
        </div>
        <SheetDescription className="truncate font-mono text-xs" title={shell.url}>
          {shell.url}
        </SheetDescription>
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="outline" onClick={test} disabled={testing}>
            {testing ? <Loader2Icon className="animate-spin" /> : <PlugZapIcon />}
            测试连接
          </Button>
          {shell.task_name ? (
            <span className="text-muted-foreground text-xs">关联任务：{shell.task_name}</span>
          ) : (
            <span className="text-muted-foreground text-xs">未关联任务</span>
          )}
          {shell.last_error && !testing && (
            <span className="max-w-full truncate text-xs text-red-500" title={shell.last_error}>
              {shell.last_error}
            </span>
          )}
        </div>
      </SheetHeader>

      <Tabs defaultValue="terminal" className="flex min-h-0 flex-1 flex-col gap-0">
        <TabsList className="mx-4 mt-3 w-fit">
          <TabsTrigger value="terminal">
            <TerminalIcon /> 终端
          </TabsTrigger>
          <TabsTrigger value="files">
            <FolderIcon /> 文件管理
          </TabsTrigger>
          <TabsTrigger value="info">
            <InfoIcon /> 信息
          </TabsTrigger>
        </TabsList>
        <TabsContent value="terminal" className="mt-3 flex min-h-0 flex-1 flex-col overflow-hidden">
          <TerminalPanel shell={shell} />
        </TabsContent>
        <TabsContent value="files" className="mt-3 flex min-h-0 flex-1 flex-col overflow-hidden">
          <FilesPanel shell={shell} />
        </TabsContent>
        <TabsContent value="info" className="mt-3 flex min-h-0 flex-1 flex-col overflow-hidden">
          <InfoPanel shell={shell} onChanged={onChanged} />
        </TabsContent>
      </Tabs>
    </>
  );
}

// ---------------------------------------------------------------------------
// 终端
// ---------------------------------------------------------------------------

type TermEntry = {
  id: number;
  cmd: string;
  output: string;
  error?: boolean;
  status?: number;
  ms?: number;
  pending?: boolean;
};

function TerminalPanel({ shell }: { shell: WebShell }) {
  const [entries, setEntries] = React.useState<TermEntry[]>([]);
  const [cmd, setCmd] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [history, setHistory] = React.useState<string[]>([]);
  const [hIndex, setHIndex] = React.useState(-1);
  const seq = React.useRef(0);
  const scrollRef = React.useRef<HTMLDivElement>(null);
  const inputRef = React.useRef<HTMLInputElement>(null);

  React.useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [entries]);

  const run = async (raw?: string) => {
    const command = (raw ?? cmd).trim();
    if (!command || busy) return;
    const id = ++seq.current;
    setEntries((prev) => [...prev, { id, cmd: command, output: "执行中…", pending: true }]);
    setBusy(true);
    setCmd("");
    setHistory((prev) => [...prev.filter((x) => x !== command), command]);
    setHIndex(-1);
    try {
      const r = await api.webShellExec(shell.id, command);
      setEntries((prev) =>
        prev.map((e) =>
          e.id === id
            ? {
                ...e,
                output: r.output || "(无输出)",
                status: r.http_status,
                ms: r.duration_ms,
                pending: false,
              }
            : e,
        ),
      );
      if (r.truncated) toast.warning("输出过长，已截断");
    } catch (e) {
      setEntries((prev) =>
        prev.map((entry) =>
          entry.id === id
            ? { ...entry, output: (e as Error).message, error: true, pending: false }
            : entry,
        ),
      );
    } finally {
      setBusy(false);
      window.requestAnimationFrame(() => inputRef.current?.focus());
    }
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowUp") {
      if (!history.length) return;
      e.preventDefault();
      const next = hIndex < 0 ? history.length - 1 : Math.max(0, hIndex - 1);
      setHIndex(next);
      setCmd(history[next]);
    } else if (e.key === "ArrowDown") {
      if (hIndex < 0) return;
      e.preventDefault();
      if (hIndex >= history.length - 1) {
        setHIndex(-1);
        setCmd("");
      } else {
        const next = hIndex + 1;
        setHIndex(next);
        setCmd(history[next]);
      }
    }
  };

  const windows = shell.os_type === "windows";
  const quick = windows
    ? ["whoami", "hostname", "ipconfig", "dir", "systeminfo | findstr /B /C:\"OS Name\""]
    : ["whoami", "id", "pwd", "uname -a", "ip addr || ifconfig"];

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center justify-between border-b px-4 py-2">
        <span className="text-muted-foreground text-xs">
          命令在目标主机执行 · 单次请求超时 {shell.timeout_seconds}s
        </span>
        <Button size="sm" variant="ghost" onClick={() => setEntries([])} disabled={!entries.length}>
          <EraserIcon /> 清空
        </Button>
      </div>

      <div ref={scrollRef} className="bg-muted/30 min-h-0 flex-1 overflow-auto p-3 font-mono text-xs">
        {entries.length === 0 && (
          <div className="text-muted-foreground space-y-1">
            <p>已连接登记信息，输入命令后回车即可在目标上执行。</p>
            <p>示例：whoami、id、pwd、uname -a、ip addr。</p>
            <p>命令输出由目标 WebShell 返回；如需上传/下载文件请切换到「文件管理」。</p>
          </div>
        )}
        {entries.map((e) => (
          <div key={e.id} className="border-border/60 border-b pb-2 last:border-0">
            <div className="flex items-center gap-2">
              <span className="text-emerald-600 dark:text-emerald-400">$</span>
              <span className="text-foreground break-all">{e.cmd}</span>
              {e.pending ? (
                <Loader2Icon className="text-muted-foreground ml-auto size-3.5 animate-spin" />
              ) : (
                <span className="text-muted-foreground ml-auto shrink-0 text-[10px]">
                  {e.status ? `HTTP ${e.status}` : ""}
                  {e.ms !== undefined ? ` · ${e.ms}ms` : ""}
                </span>
              )}
            </div>
            <pre
              className={cn(
                "mt-1 whitespace-pre-wrap break-all",
                e.error ? "text-destructive" : "text-foreground/90",
              )}
            >
              {e.output}
            </pre>
          </div>
        ))}
      </div>

      <div className="border-t p-3">
        <div className="mb-2 flex flex-wrap gap-1">
          {quick.map((q) => (
            <Button
              key={q}
              size="sm"
              variant="outline"
              className="h-6 px-2 font-mono text-[10px]"
              onClick={() => {
                setCmd(q);
                inputRef.current?.focus();
              }}
              disabled={busy}
            >
              {q}
            </Button>
          ))}
        </div>
        <form
          className="flex items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            void run();
          }}
        >
          <Input
            ref={inputRef}
            value={cmd}
            onChange={(e) => setCmd(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder="输入命令，Enter 执行（↑/↓ 历史）"
            className="font-mono"
            autoFocus
            disabled={busy}
          />
          <Button type="submit" disabled={busy || !cmd.trim()}>
            {busy ? <Loader2Icon className="animate-spin" /> : <PlayIcon />}
            执行
          </Button>
        </form>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 文件管理
// ---------------------------------------------------------------------------

type EditorState = {
  file: WebShellFileEntry;
  data?: WebShellFileData;
  text: string;
  binary: boolean;
  dirty: boolean;
  saving: boolean;
  loading: boolean;
};

function FilesPanel({ shell }: { shell: WebShell }) {
  const [listing, setListing] = React.useState<WebShellFileListing | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [error, setError] = React.useState("");
  const [pathInput, setPathInput] = React.useState("");
  const [editor, setEditor] = React.useState<EditorState | null>(null);
  const [mkdirOpen, setMkdirOpen] = React.useState(false);
  const [mkdirName, setMkdirName] = React.useState("");
  const [renameTarget, setRenameTarget] = React.useState<WebShellFileEntry | null>(null);
  const [renameValue, setRenameValue] = React.useState("");
  const [fileBusy, setFileBusy] = React.useState(false);
  const uploadRef = React.useRef<HTMLInputElement>(null);

  const windows = (listing?.os_type ?? shell.os_type) === "windows";

  const load = React.useCallback(
    async (p: string) => {
      setLoading(true);
      setError("");
      try {
        const l = await api.webShellFiles(shell.id, p);
        setListing(l);
        setPathInput(l.path);
      } catch (e) {
        setError((e as Error).message);
      } finally {
        setLoading(false);
      }
    },
    [shell.id],
  );

  React.useEffect(() => {
    void load("");
  }, [load]);

  const openEditor = async (entry: WebShellFileEntry) => {
    setEditor({
      file: entry,
      data: undefined,
      text: "",
      binary: false,
      dirty: false,
      saving: false,
      loading: true,
    });
    try {
      const d = await api.webShellReadFile(shell.id, entry.path, 0, EDIT_READ_BYTES);
      const bytes = b64ToBytes(d.data_b64);
      const decoded = decodeTextBytes(bytes);
      setEditor({
        file: entry,
        data: d,
        text: decoded.text,
        binary: decoded.binary || d.binary,
        dirty: false,
        saving: false,
        loading: false,
      });
    } catch (e) {
      setEditor(null);
      toast.error(`读取文件失败：${(e as Error).message}`);
    }
  };

  const downloadFile = async (entry: WebShellFileEntry) => {
    try {
      const d = await api.webShellReadFile(shell.id, entry.path, 0, DOWNLOAD_READ_BYTES);
      downloadBytes(b64ToBytes(d.data_b64), entry.name);
      if (d.truncated) toast.warning("文件超过 4MB，仅下载了前 4MB；可下载后用其他方式获取完整文件");
    } catch (e) {
      toast.error(`下载失败：${(e as Error).message}`);
    }
  };

  const saveEditor = async () => {
    if (!editor?.data) return;
    setEditor({ ...editor, saving: true });
    try {
      const bytes = new TextEncoder().encode(editor.text);
      await api.webShellWriteFile(shell.id, editor.file.path, bytesToB64(bytes));
      toast.success("已保存");
      setEditor(null);
      void load(listing?.path ?? "");
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
      setEditor((cur) => (cur ? { ...cur, saving: false } : cur));
    }
  };

  const doMkdir = async () => {
    const name = mkdirName.trim();
    if (!name) return;
    setFileBusy(true);
    try {
      await api.webShellMkdir(shell.id, joinRemotePath(listing?.path ?? "", name, windows));
      toast.success("已创建目录");
      setMkdirOpen(false);
      setMkdirName("");
      void load(listing?.path ?? "");
    } catch (e) {
      toast.error(`创建失败：${(e as Error).message}`);
    } finally {
      setFileBusy(false);
    }
  };

  const doRename = async () => {
    if (!renameTarget) return;
    const name = renameValue.trim();
    if (!name) return;
    setFileBusy(true);
    try {
      await api.webShellRename(
        shell.id,
        renameTarget.path,
        joinRemotePath(listing?.path ?? "", name, windows),
      );
      toast.success("已重命名");
      setRenameTarget(null);
      void load(listing?.path ?? "");
    } catch (e) {
      toast.error(`重命名失败：${(e as Error).message}`);
    } finally {
      setFileBusy(false);
    }
  };

  const doDelete = async (entry: WebShellFileEntry) => {
    const extra = entry.dir ? "（含其下所有内容）" : "";
    if (!window.confirm(`确认删除${entry.dir ? "目录" : "文件"} “${entry.name}”？${extra}`)) return;
    try {
      await api.webShellDeleteFile(shell.id, entry.path);
      toast.success("已删除");
      void load(listing?.path ?? "");
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  };

  const doUpload = async (files: FileList | null) => {
    if (!files?.length) return;
    const dir = listing?.path ?? "";
    let done = 0;
    for (const file of Array.from(files)) {
      if (file.size > MAX_UPLOAD) {
        toast.error(`${file.name} 超过 8MB，已跳过`);
        continue;
      }
      try {
        const bytes = new Uint8Array(await file.arrayBuffer());
        await api.webShellWriteFile(shell.id, joinRemotePath(dir, file.name, windows), bytesToB64(bytes));
        done++;
      } catch (e) {
        toast.error(`${file.name} 上传失败：${(e as Error).message}`);
      }
    }
    if (uploadRef.current) uploadRef.current.value = "";
    if (done > 0) {
      toast.success(`已上传 ${done} 个文件`);
      void load(dir);
    }
  };

  const canGoUp = !!listing?.parent;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b px-4 py-2">
        <Button
          size="icon-sm"
          variant="ghost"
          title="上级目录"
          disabled={!canGoUp || loading}
          onClick={() => listing && void load(listing.parent)}
        >
          <ArrowUpIcon className="size-4" />
        </Button>
        <Input
          value={pathInput}
          onChange={(e) => setPathInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") void load(pathInput);
          }}
          className="h-8 min-w-0 flex-1 font-mono text-xs"
          placeholder="目标路径"
        />
        <Button size="sm" variant="outline" onClick={() => void load(pathInput)} disabled={loading}>
          前往
        </Button>
        <Button
          size="icon-sm"
          variant="ghost"
          title="刷新"
          onClick={() => void load(listing?.path ?? "")}
          disabled={loading}
        >
          <RefreshCwIcon className={cn("size-4", loading && "animate-spin")} />
        </Button>
        <Button size="sm" variant="outline" onClick={() => setMkdirOpen(true)} disabled={!listing}>
          <FolderPlusIcon /> 新建目录
        </Button>
        <Button size="sm" variant="outline" onClick={() => uploadRef.current?.click()} disabled={!listing}>
          <UploadIcon /> 上传
        </Button>
        <input
          ref={uploadRef}
          type="file"
          multiple
          className="hidden"
          onChange={(e) => void doUpload(e.target.files)}
        />
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {error ? (
          <div className="flex flex-col items-center gap-3 p-10 text-center">
            <p className="text-destructive text-sm">{error}</p>
            <Button size="sm" variant="outline" onClick={() => void load(listing?.path ?? "")}>
              <RefreshCwIcon /> 重试
            </Button>
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead className="w-24 text-right">大小</TableHead>
                <TableHead className="w-36">修改时间</TableHead>
                <TableHead className="w-28 text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {!listing || (loading && listing.entries.length === 0) ? (
                <TableRow>
                  <TableCell colSpan={4} className="text-muted-foreground py-10 text-center text-sm">
                    <Loader2Icon className="mr-1 inline size-4 animate-spin" /> 读取目录…
                  </TableCell>
                </TableRow>
              ) : listing.entries.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={4} className="text-muted-foreground py-10 text-center text-sm">
                    空目录
                  </TableCell>
                </TableRow>
              ) : (
                listing.entries.map((entry) => (
                  <TableRow key={entry.path}>
                    <TableCell>
                      <button
                        type="button"
                        className="flex max-w-full items-center gap-2 text-left hover:underline"
                        onClick={() => (entry.dir ? void load(entry.path) : void openEditor(entry))}
                      >
                        {entry.dir ? (
                          <FolderIcon className="size-4 shrink-0 text-blue-500" />
                        ) : (
                          <FileIcon className="text-muted-foreground size-4 shrink-0" />
                        )}
                        <span className="truncate font-mono text-sm">{entry.name}</span>
                      </button>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-right font-mono text-xs">
                      {entry.dir ? "—" : fmtSize(entry.size)}
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">{fmtTime(entry.mtime)}</TableCell>
                    <TableCell>
                      <div className="flex items-center justify-end gap-0.5">
                        {!entry.dir && (
                          <Button
                            size="icon-sm"
                            variant="ghost"
                            title="下载"
                            onClick={() => void downloadFile(entry)}
                          >
                            <DownloadIcon className="size-3.5" />
                          </Button>
                        )}
                        <Button
                          size="icon-sm"
                          variant="ghost"
                          title="重命名"
                          onClick={() => {
                            setRenameTarget(entry);
                            setRenameValue(entry.name);
                          }}
                        >
                          <PencilIcon className="size-3.5" />
                        </Button>
                        <Button
                          size="icon-sm"
                          variant="ghost"
                          className="text-destructive"
                          title="删除"
                          onClick={() => void doDelete(entry)}
                        >
                          <Trash2Icon className="size-3.5" />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        )}
      </div>

      {/* 文件查看 / 编辑 */}
      <Dialog open={editor !== null} onOpenChange={(o) => !o && !editor?.saving && setEditor(null)}>
        <DialogContent className="flex max-h-[90vh] flex-col sm:max-w-3xl">
          {editor && (
            <>
              <DialogHeader>
                <DialogTitle className="flex items-center gap-2 pr-8 font-mono text-sm">
                  <FileIcon className="size-4 shrink-0" />
                  <span className="truncate" title={editor.file.path}>
                    {editor.file.path}
                  </span>
                </DialogTitle>
                <DialogDescription>
                  {editor.data ? `${fmtSize(editor.data.size)} · ${editor.dirty ? "未保存的修改" : "已同步"}` : "读取中…"}
                  {editor.data?.truncated ? " · 仅加载了前 1MB，已禁用保存" : ""}
                </DialogDescription>
              </DialogHeader>

              {editor.loading ? (
                <div className="text-muted-foreground flex flex-1 items-center justify-center gap-2 py-16 text-sm">
                  <Loader2Icon className="size-4 animate-spin" /> 读取文件…
                </div>
              ) : editor.binary ? (
                <div className="flex flex-1 flex-col items-center justify-center gap-3 py-16 text-center">
                  <p className="text-muted-foreground text-sm">二进制文件，不支持在线编辑。</p>
                  <Button variant="outline" onClick={() => void downloadFile(editor.file)}>
                    <DownloadIcon /> 下载文件
                  </Button>
                </div>
              ) : (
                <>
                  <div className="min-h-0 flex-1 overflow-hidden">
                    <Textarea
                      value={editor.text}
                      onChange={(e) => setEditor({ ...editor, text: e.target.value, dirty: true })}
                      spellCheck={false}
                      disabled={editor.data?.truncated}
                      className="h-[50vh] resize-none font-mono text-xs leading-relaxed"
                    />
                  </div>
                  <DialogFooter className="items-center justify-between sm:justify-between">
                    <Button variant="outline" onClick={() => void downloadFile(editor.file)}>
                      <DownloadIcon /> 下载
                    </Button>
                    <div className="flex gap-2">
                      <Button variant="outline" onClick={() => setEditor(null)} disabled={editor.saving}>
                        取消
                      </Button>
                      <Button
                        onClick={() => void saveEditor()}
                        disabled={!editor.dirty || editor.saving || editor.data?.truncated}
                      >
                        {editor.saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />}
                        保存
                      </Button>
                    </div>
                  </DialogFooter>
                </>
              )}
            </>
          )}
        </DialogContent>
      </Dialog>

      {/* 新建目录 */}
      <Dialog open={mkdirOpen} onOpenChange={(o) => !fileBusy && setMkdirOpen(o)}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>新建目录</DialogTitle>
            <DialogDescription className="font-mono text-xs">{listing?.path || "."}</DialogDescription>
          </DialogHeader>
          <Input
            autoFocus
            value={mkdirName}
            onChange={(e) => setMkdirName(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && void doMkdir()}
            placeholder="目录名称"
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setMkdirOpen(false)} disabled={fileBusy}>
              取消
            </Button>
            <Button onClick={() => void doMkdir()} disabled={!mkdirName.trim() || fileBusy}>
              {fileBusy && <Loader2Icon className="animate-spin" />}
              创建
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 重命名 */}
      <Dialog open={renameTarget !== null} onOpenChange={(o) => !fileBusy && !o && setRenameTarget(null)}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>重命名</DialogTitle>
            <DialogDescription className="truncate font-mono text-xs">{renameTarget?.path}</DialogDescription>
          </DialogHeader>
          <Input
            autoFocus
            value={renameValue}
            onChange={(e) => setRenameValue(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && void doRename()}
            placeholder="新名称"
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setRenameTarget(null)} disabled={fileBusy}>
              取消
            </Button>
            <Button onClick={() => void doRename()} disabled={!renameValue.trim() || fileBusy}>
              {fileBusy && <Loader2Icon className="animate-spin" />}
              确定
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 信息面板
// ---------------------------------------------------------------------------

function InfoPanel({ shell, onChanged }: { shell: WebShell; onChanged: () => void }) {
  const [info, setInfo] = React.useState<WebShellInfo | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [error, setError] = React.useState("");
  const [testing, setTesting] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setInfo(await api.webShellInfo(shell.id));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [shell.id]);

  React.useEffect(() => {
    void load();
  }, [load]);

  const test = async () => {
    setTesting(true);
    try {
      const r = await api.testWebShell(shell.id);
      if (r.ok) toast.success(`连接正常${r.os_type ? `（${osLabel(r.os_type)}）` : ""}`);
      else toast.error(r.error || "测试失败");
      onChanged();
      void load();
    } catch (e) {
      toast.error(`测试失败：${(e as Error).message}`);
    } finally {
      setTesting(false);
    }
  };

  return (
    <div className="min-h-0 flex-1 overflow-auto p-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm font-medium">目标信息</span>
        <div className="flex gap-2">
          <Button size="sm" variant="outline" onClick={() => void load()} disabled={loading}>
            <RefreshCwIcon className={cn(loading && "animate-spin")} /> 刷新
          </Button>
          <Button size="sm" variant="outline" onClick={() => void test()} disabled={testing}>
            {testing ? <Loader2Icon className="animate-spin" /> : <PlugZapIcon />} 测试连接
          </Button>
        </div>
      </div>

      {error ? (
        <div className="rounded-lg border border-red-500/30 bg-red-500/5 p-3 text-sm text-red-600 dark:text-red-400">
          {error}
        </div>
      ) : loading && !info ? (
        <div className="text-muted-foreground flex items-center justify-center gap-2 py-10 text-sm">
          <Loader2Icon className="size-4 animate-spin" /> 获取目标信息…
        </div>
      ) : info ? (
        <div className="grid gap-3 sm:grid-cols-2">
          <InfoCard label="当前用户" value={info.user} mono />
          <InfoCard label="主机名" value={info.hostname} mono />
          <InfoCard label="当前目录" value={info.pwd} mono />
          <InfoCard label="IP 地址" value={info.ip} mono />
          <InfoCard label="操作系统" value={info.os} wide />
          <InfoCard label="传输编码" value={info.transport || "—"} mono />
        </div>
      ) : null}

      <div className="mt-6">
        <h3 className="mb-2 text-sm font-medium">登记信息</h3>
        <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
          <InfoRow label="地址" value={shell.url} mono />
          <InfoRow label="类型" value={typeLabel(shell.type)} />
          <InfoRow label="语言 / 方式" value={`${shell.lang || "—"} / ${shell.mode || "—"}`} />
          <InfoRow label="关联任务" value={shell.task_name || "未关联"} />
          <InfoRow label="登记来源" value={shell.created_by === "agent" ? "Agent 自动登记" : "手动添加"} />
          <InfoRow label="创建时间" value={fmtTime(shell.created_at)} />
          <InfoRow label="最近使用" value={fmtTime(shell.last_used_at)} />
          <InfoRow label="最近检测" value={fmtTime(shell.last_checked_at)} />
          {shell.notes && <InfoRow label="备注" value={shell.notes} wide />}
        </dl>
      </div>

      {info?.raw && (
        <details className="mt-6">
          <summary className="text-muted-foreground hover:text-foreground cursor-pointer text-xs">
            原始信息输出
          </summary>
          <pre className="bg-muted/40 mt-2 max-h-60 overflow-auto rounded-lg p-3 font-mono text-xs whitespace-pre-wrap">
            {info.raw}
          </pre>
        </details>
      )}
    </div>
  );
}

function InfoCard({ label, value, mono, wide }: { label: string; value: string; mono?: boolean; wide?: boolean }) {
  return (
    <div className={cn("rounded-lg border p-3", wide && "sm:col-span-2")}>
      <div className="text-muted-foreground text-xs">{label}</div>
      <div className={cn("mt-1 break-all whitespace-pre-wrap", mono ? "font-mono text-xs" : "text-sm")}>
        {value || "—"}
      </div>
    </div>
  );
}

function InfoRow({ label, value, mono, wide }: { label: string; value: string; mono?: boolean; wide?: boolean }) {
  return (
    <div className={cn("flex min-w-0 gap-2", wide && "sm:col-span-2")}>
      <dt className="text-muted-foreground w-20 shrink-0 text-xs">{label}</dt>
      <dd className={cn("min-w-0 break-all text-sm", mono && "font-mono text-xs")}>{value || "—"}</dd>
    </div>
  );
}
