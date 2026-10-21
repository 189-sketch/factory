import Markdown from "react-markdown";
import { Tabs } from "@/components/ui/tabs";
import { useEffect, useState } from "react";
import { Server } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { PageHeading, QuietState } from "@/components/ui/page-heading";

export function WorkersPage({ workers, loaded, error }) {
  return <Page title="Runners" description="Agents execute on local hosts, VMs or Kubernetes."><p className="text-sm"><a href="#/analytics" className="text-primary underline">View usage and task analytics</a></p>{error && <Failure value={error} />}{!loaded && !error ? <Loading description="Checking live worker status." /> : loaded && (workers.length ? <Card className="overflow-hidden">{workers.map((worker) => <article key={worker.instance_id} className="grid gap-4 border-b border-border p-4 last:border-b-0 sm:grid-cols-[minmax(12rem,1fr)_minmax(12rem,1fr)_10rem] sm:items-center sm:px-5">
    <div className="min-w-0"><div className="flex items-center gap-2"><Server className="size-4 text-muted-foreground" /><h2 className="truncate text-sm font-medium">{worker.name}</h2></div><p className="mt-1 truncate font-mono text-xs text-muted-foreground">{worker.instance_id}</p></div>
    <div className="flex flex-wrap gap-1.5">{worker.repositories?.length ? worker.repositories.map((repository) => <Badge key={repository} className="border-border bg-muted font-mono text-muted-foreground">{repository}</Badge>) : <span className="text-xs text-muted-foreground">No repositories</span>}</div>
    <div className="flex items-center justify-between gap-2 sm:flex-col sm:items-end"><Badge className={worker.connected ? "gap-1.5 border-success/25 bg-success/10 text-success" : "gap-1.5 border-border bg-muted text-muted-foreground"}><span className="size-1.5 rounded-full bg-current" />{worker.connected ? "Connected" : "Disconnected"}</Badge><time className="text-xs text-muted-foreground sm:text-right" dateTime={worker.last_seen_at} title={new Date(worker.last_seen_at).toLocaleString()}>Last seen {relativeTime(worker.last_seen_at)}</time></div>
  </article>)}</Card> : <Empty value="No workers registered." description="Start a worker to register this machine with the control plane." />)}</Page>;
}

const displayName = name => String(name).replaceAll("_", " ").replaceAll("-", " ").replace(/^./, c => c.toUpperCase());

function Page({ title, description, children }) { return <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8"><PageHeading title={title} description={description} />{children}</div>; }
function Loading({ description = "Loading the latest configuration." }) { return <Card><QuietState title="Loading configuration" description={description} role="status" /></Card>; }
function Empty({ value, description = "Configuration added on the control plane will appear here." }) { return <Card><QuietState title={value} description={description} /></Card>; }
function Failure({ value }) { return <div role="alert" className="rounded-md border border-danger/35 bg-danger/10 px-3 py-2 text-sm text-danger">{value}</div>; }

function useDefinitions() {
  const [result, setResult] = useState({ loading: true, error: "", value: { commands: [] } });
  useEffect(() => {
    const controller = new AbortController();
    setResult(current => ({ ...current, loading: true, error: "" }));
    fetch("/api/v1/definitions", { headers: { Accept: "application/json" }, signal: controller.signal }).then(async (response) => {
      if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(body.error || `Definitions request failed (${response.status})`); }
      return response.json();
    }).then((value) => setResult({ loading: false, error: "", value })).catch((error) => { if (error.name !== "AbortError") setResult((current) => ({ ...current, loading: false, error: error.message })); });
    return () => controller.abort();
  }, []);
  return result;
}

function relativeTime(value) { const seconds = Math.max(0, Math.floor((Date.now() - Date.parse(value)) / 1000)); if (seconds < 10) return "just now"; if (seconds < 60) return `${seconds}s ago`; const minutes = Math.floor(seconds / 60); if (minutes < 60) return `${minutes}m ago`; const hours = Math.floor(minutes / 60); if (hours < 24) return `${hours}h ago`; return `${Math.floor(hours / 24)}d ago`; }

const specialistRoles = [
  ["triage", "Research the request, inspect the repository, and identify what is ready to build."],
  ["spec", "Resolve consequential choices and write a specification for your approval."],
  ["implement", "Build and verify the change, then create or update its pull request."],
  ["review", "Independently check the change and return definite findings without editing it."],
];

export function LegacyAgentsPage({ csrfToken, stateBackend }) {
  const factoryBackend = ["github", "local"].includes(stateBackend);
  const definitions = useDefinitions();
  const [selected, setSelected] = useState("triage");
  const [drafts,setDrafts]=useState({});
  const draftKey=selected;
  const draftState = drafts[draftKey];
  const draft = draftState?.text ?? null;
  function setDraft(value) {
    setDrafts(current => ({ ...current, [draftKey]: value === null ? null : {
      text: value,
      baseHash: current[draftKey]?.baseHash ?? savedHashes[draftKey] ?? command?.hash ?? "",
    } }));
  }
  const [savedHashes, setSavedHashes] = useState({});
  const [savedPrompts, setSavedPrompts] = useState({});
  const [saveState, setSaveState] = useState("");
  const command = definitions.value.commands.find(item => item.name === selected);
  const role = specialistRoles.find(([name]) => name === selected);
  async function savePrompt() {
    setSaveState("Saving…");
    try {
      const response = await fetch(`/api/v1/factory/agents/${selected}`, { method: "PUT", headers: { "Content-Type": "application/json", "X-Machinist-CSRF": csrfToken }, body: JSON.stringify({ prompt: draft ?? savedPrompts[draftKey] ?? command?.prompt ?? "", expected_hash: draftState?.baseHash ?? savedHashes[draftKey] ?? command?.hash ?? "" }) });
      const body = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(response.status === 409 ? "This prompt changed elsewhere. Reload Agents before saving your changes." : body.error || `Unable to save prompt (${response.status})`);
      setSavedPrompts(current => ({ ...current, [draftKey]: draft ?? command?.prompt ?? "" }));
      setSavedHashes(current => ({ ...current, [draftKey]: body.hash || body.command?.hash || command?.hash }));
      setDraft(null);
      setSaveState(["github", "local"].includes(stateBackend) ? "Saved. Future stage attempts use this Markdown version." : "Saved. Future attempts use this version.");
    } catch (error) { setSaveState(error.message); }
  }
  return <Page title="Agents" description="Reusable agent instructions. Choose the project when you start a job.">

    {definitions.loading ? <Loading /> : definitions.error ? <Failure value={definitions.error} /> : <div className="agent-layout">
      <section className="agent-list" aria-label="Factory agents">{specialistRoles.map(([name, purpose]) => <button disabled={saveState === "Saving…"} key={name} className={`agent-row ${selected === name ? "agent-row-selected" : ""}`} onClick={() => { setSelected(name); setSaveState(""); }} aria-pressed={selected === name}><span className="text-sm font-semibold">{displayName(name)}</span><span className="mt-1 block text-xs leading-5 text-muted-foreground">{purpose}</span><span className="mt-3 block text-xs text-muted-foreground">{definitions.value.commands.find(item => item.name === name)?.executor || "Not configured"}</span></button>)}</section>
      <section className="agent-detail min-w-0"><header className="mb-6"><h2 className="text-lg font-semibold">{displayName(selected)}</h2><p className="mt-2 text-sm leading-6 text-muted-foreground">{role[1]}</p></header><Tabs key={selected} label={`${displayName(selected)} information`} items={[
        { id: "prompt", label: "Prompt", content: <article className="markdown-prompt"><Markdown>{draft ?? savedPrompts[draftKey] ?? command?.prompt ?? ""}</Markdown></article> },
        { id: "edit", label: "Edit prompt", content: <div className="space-y-4"><p className="text-xs text-muted-foreground">Write instructions in Markdown. Existing attempts keep their original prompt.</p>{!factoryBackend ? <p className="text-sm text-muted-foreground">This legacy backend uses configuration files for command prompts. Use factory mode for agent editing.</p> : command ? <><label className="block"><span className="sr-only">{displayName(selected)} prompt</span><textarea disabled={saveState === "Saving…"} className="field-control agent-prompt" value={draft ?? savedPrompts[draftKey] ?? command.prompt ?? ""} onChange={event => setDraft(event.target.value)} spellCheck={false} /></label><div className="flex flex-wrap items-center gap-3"><button className="prompt-save" onClick={savePrompt} disabled={draft === null || saveState === "Saving…"}>Save prompt</button><p role="status" className="text-xs text-muted-foreground">{saveState}</p></div></> : <p className="text-sm text-muted-foreground">This specialist is not configured. Enable the factory workflow to load its prompt.</p>}</div> },
        { id: "automations", label: "Automations", content: <div className="space-y-4 text-sm"><p className="text-muted-foreground">The factory routes this specialist automatically as work progresses. GitHub triggers can submit tasks to that workflow.</p><a className="text-primary underline" href="#/automations">View automations</a></div> },
        { id: "settings", label: "Settings", content: <dl className="grid gap-5 text-sm"><div><dt className="text-muted-foreground">Executor</dt><dd className="mt-1">{command?.executor || "Not configured"}</dd></div><div><dt className="text-muted-foreground">Execution timeout</dt><dd className="mt-1">{command?.timeout || "Not configured"}</dd></div><div><dt className="text-muted-foreground">Execution host</dt><dd className="mt-1">The task’s assigned worker. Local hosts and remote VMs use the same pipeline.</dd></div><div><a className="text-primary underline" href="#/settings">View worker settings</a></div></dl> },
      ]} /></section>
    </div>}
  </Page>;
}

function NativeAgentsPage() {
  const [repository, setRepository] = useState("");
  const [selected, setSelected] = useState("primary");
  const [revision, setRevision] = useState(0);
  const [result, setResult] = useState({ loading: true, error: "", data: { agents: [], repositories: [] } });
  useEffect(() => {
    let active = true;
    const controller = new AbortController();
    setResult(current => ({ ...current, loading: true, error: "" }));
    const query = repository ? `?repository=${encodeURIComponent(repository)}` : "";
    fetch(`/api/v1/agents${query}`, { headers: { Accept: "application/json" }, signal: controller.signal }).then(async response => {
      const body = await response.json();
      if (!response.ok) throw new Error(body.error || "Unable to read configured agents.");
      if (!Array.isArray(body.agents) || !Array.isArray(body.repositories)) throw new Error("Invalid agent configuration response.");
      return body;
    }).then(data => { if (active) setResult({ loading: false, error: "", data }); }).catch(error => {
      if (active && error.name !== "AbortError") setResult(current => ({ ...current, loading: false, error: error.message }));
    });
    return () => { active = false; controller.abort(); };
  }, [repository, revision]);
  const { data, loading, error } = result;
  const agents = data.agents || [];
  const agent = agents.find(item => item.id === selected) || agents[0];
  const harnessName = name => name === "claude" ? "Claude Code" : name === "codex" ? "Codex" : name || "Not selected";
  const modelName = item => !item.model || item.model === "inherit" ? item.kind === "primary" ? `${harnessName(item.harness)} default` : "Inherited from primary" : item.model;
  return <Page title="Agents" description="The primary prompt and native agents configured for your repository.">
    <div className="agent-catalog-toolbar"><div><h2>{data.repository || "Configured agents"}</h2><p>{harnessName(data.harness)} · Read-only configuration</p></div><button type="button" className="agent-catalog-refresh" disabled={loading} onClick={() => setRevision(value => value + 1)}>{loading ? "Loading…" : "Refresh"}</button></div>
    {data.repositories.length > 1 && <nav aria-label="Agent repositories" className="agent-repositories">{data.repositories.map(name => <button type="button" key={name} aria-pressed={name === (repository || data.repository)} onClick={() => { setSelected("primary"); setRepository(name); }}>{name}</button>)}</nav>}
    {error ? <Failure value={error} /> : loading ? <Loading description="Reading the primary prompt and native agent files." /> : <>
      {data.notice && <p role="status" className="agent-catalog-notice">{data.notice}</p>}
      {!agents.length ? <Empty value="No agents configured" description="Configure the primary agent before starting a run." /> : <div className="agent-layout configured-agent-layout">
        <nav className="agent-list" aria-label="Configured agents">{agents.map(item => <button type="button" key={item.id} className={`agent-row ${agent?.id === item.id ? "agent-row-selected" : ""}`} aria-pressed={agent?.id === item.id} onClick={() => setSelected(item.id)}><span className="agent-catalog-name">{item.name}</span><span className="agent-catalog-role">{item.kind === "primary" ? "Primary" : "Subagent"}</span><span className="agent-catalog-model">{modelName(item)}</span>{item.error && <span className="text-danger text-xs">Definition needs attention</span>}</button>)}
          {agents.length === 1 && !data.notice && <p className="agent-catalog-empty">No repository subagents are configured for {harnessName(data.harness)}. Built-in and plugin agents are managed by the harness.</p>}
        </nav>
        {agent && <section className="agent-detail min-w-0" aria-label="Selected agent"><header className="agent-catalog-header"><h2>{agent.name}</h2>{agent.description && <p>{agent.description}</p>}</header>
          <dl className="agent-catalog-facts"><div><dt>Model</dt><dd>{modelName(agent)}</dd></div><div><dt>Harness</dt><dd>{harnessName(agent.harness)}</dd></div><div><dt>Source</dt><dd><code>{agent.source || "Not configured"}</code></dd></div></dl>
          {agent.error && <Failure value={agent.error} />}
          <section className="agent-catalog-prompt"><h3>{agent.kind === "primary" ? "Primary prompt template" : "Agent prompt"}</h3>{agent.prompt ? <article className="run-prose"><Markdown skipHtml>{agent.prompt}</Markdown></article> : <p className="text-sm text-muted-foreground">No prompt could be read from this definition.</p>}</section>
        </section>}
      </div>}
    </>}
  </Page>;
}

export function AgentsPage(props) {
  return ["github", "local"].includes(props.stateBackend) ? <NativeAgentsPage /> : <LegacyAgentsPage {...props} />;
}
