import { useEffect, useRef, useState } from "react";
import { ArrowUpRight, GitBranch, Plus, RefreshCw, X } from "lucide-react";
import { Button } from "./components/ui/button";
import { PageHeading } from "./components/ui/page-heading";
import { jobDisplayTitle } from "./runs-board.js";
import { State } from "./task-display.jsx";

export function ProjectsPage({ status, onRefresh, onStart }) {
  const [open, setOpen] = useState(false);
  const [editingDefaults, setEditingDefaults] = useState("");
  const defaultsPanel = useRef(null);
  const defaultsOpener = useRef(null);
  useEffect(() => { if (editingDefaults) defaultsPanel.current?.focus(); }, [editingDefaults]);
  const [available, setAvailable] = useState([]);
  const [repository, setRepository] = useState("");
  const [path, setPath] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const opener = useRef(null);
  const editor = useRef(null);
  const errorRef = useRef(null);
  const loadingRequest = useRef(null);
  const repositories = status.repositories || [];
  const configured = new Set(repositories.map(name => (status.repository_mappings?.[name] || name).toLowerCase()));
  const choices = available.filter(repo => !configured.has(repo.full_name.toLowerCase()));
  const canAdd = status.state_backend === "local" && status.onboarding?.github?.connected;
  const host = status.onboarding?.selection?.host;
  const running = status.jobs?.some(job => job.state === "running");
  const installURL = status.onboarding?.github?.install_url;
  async function load() {
    loadingRequest.current?.abort();
    const controller = new AbortController(); loadingRequest.current = controller;
    const signal = controller.signal;
    setLoading(true); setError("");
    try {
      const response = await fetch("/api/v1/onboarding/repositories", { headers: { Accept: "application/json" }, signal });
      const result = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(result.error || `Could not load repositories (${response.status}). Try again.`);
      if (signal.aborted || loadingRequest.current !== controller) return;
      const repos = Array.isArray(result) ? result : [];
      setAvailable(repos);
      const permitted = repos.filter(repo => !configured.has(repo.full_name.toLowerCase()) && repo.can_write !== false);
      setRepository(current => permitted.some(repo => repo.full_name === current) ? current : permitted[0]?.full_name || "");
    } catch (e) { if (e.name !== "AbortError" && loadingRequest.current === controller) setError(e.message); }
    finally { if (!signal.aborted && loadingRequest.current === controller) setLoading(false); }
  }
  useEffect(() => {
    if (!open) return;
    load();
    return () => loadingRequest.current?.abort();
  }, [open]);
  useEffect(() => { if (open) editor.current?.focus(); else opener.current?.focus(); }, [open]);
  useEffect(() => { if (error) errorRef.current?.focus(); }, [error]);
  function close() { setOpen(false); setError(""); setPath(""); }
  async function add(event) {
    event.preventDefault(); if (saving) return;
    setSaving(true); setError(""); setNotice("");
    try {
      const response = await fetch("/api/v1/projects", { method: "POST", headers: { "Content-Type": "application/json", "X-Machinist-CSRF": status.csrf_token }, body: JSON.stringify({ repository, path: path.trim() }) });
      const result = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(result.error || `Could not add project (${response.status}). Try again.`);
      setNotice(`${repository} added.${result.remote_setup_required ? " Stop your VM worker, then run the updated VM setup to include this project." : " Your existing projects and agent settings are unchanged."}`);
      close(); await onRefresh?.();
    } catch (e) { setError(e.message); }
    finally { setSaving(false); }
  }
  return <section className="runs-page projects-page">
    <PageHeading title="Projects" description="Connect repositories to your local, VM or Kubernetes agents.">
      <div className="flex flex-wrap items-center gap-2"><Button asChild variant="ghost"><a href="#/setup">Connection settings</a></Button>{canAdd && <Button ref={opener} disabled={open} onClick={() => { setNotice(""); setOpen(true); }}><Plus className="size-4" />Add project</Button>}</div>
    </PageHeading>
    {notice && <p className="project-notice" role="status">{notice}</p>}
    {open && <section className="project-editor" aria-label="Add project" tabIndex={-1} ref={editor}>
      <header><h2>Add project</h2><Button variant="ghost" size="icon" aria-label="Close project editor" disabled={saving} onClick={close}><X className="size-4" /></Button></header>
      <form onSubmit={add} aria-busy={saving}>
        {error && <p role="alert" className="project-error" ref={errorRef} tabIndex={-1}>{error}</p>}
        <p className="project-hint">Only repositories shared with your Machinist GitHub App appear here. {installURL && <a href={installURL} target="_blank" rel="noreferrer">Manage GitHub access<ArrowUpRight className="size-3" /></a>}</p>
        <label><span>GitHub repository</span><select className="field-control" required disabled={loading || saving || !choices.length} value={repository} onChange={event => setRepository(event.target.value)}>{!choices.length && <option value="">{loading ? "Loading repositories…" : "No additional repositories available"}</option>}{choices.map(repo => <option key={repo.full_name} value={repo.full_name} disabled={repo.can_write === false}>{repo.full_name}{repo.can_write === false ? " · Write access required" : repo.private ? " · Private" : ""}</option>)}</select></label>
        <div className="project-refresh"><Button variant="ghost" type="button" size="sm" disabled={loading || saving} onClick={() => load()}><RefreshCw className="size-3.5" />{loading ? "Refreshing…" : "Refresh repositories"}</Button>{!loading && !choices.length && <p>Grant access to another repository on GitHub, then refresh.</p>}</div>
        <label><span>Checkout path <small>Optional</small></span><input className="field-control" readOnly={saving} value={path} onChange={event => setPath(event.target.value)} placeholder="Leave blank for a managed clone" /></label>
        <p className="project-hint">Each project has its own checkout. All projects share your selected agent and execution target.{host === "remote" ? " After adding a project, stop your VM worker and run the updated VM setup." : " The managed runner reloads its project list when you add one."}</p>
        {running && <p className="project-hint text-warning">Wait for running jobs to finish before adding a project.</p>}
        <footer><Button type="button" variant="ghost" disabled={saving} onClick={close}>Cancel</Button><Button type="submit" disabled={loading || saving || !repository || running}>{saving ? "Preparing project…" : "Add project"}</Button></footer>
      </form>
    </section>}
    {host === "remote" && repositories.length > 1 && <p className="project-hint project-vm-setup"><a href="/api/v1/onboarding/worker-config?host=remote" download>Download VM setup for all projects</a><span>Stop the existing worker before running this setup on your VM. It contains a private worker connection credential.</span></p>}
    {editingDefaults && repositories.includes(editingDefaults) && <section className="project-default-panel" aria-label="Project defaults" tabIndex={-1} ref={defaultsPanel}>
      <ProjectDefaults key={editingDefaults} repository={editingDefaults} defaults={status.onboarding?.project_defaults?.[editingDefaults]} host={host} csrfToken={status.csrf_token} onRefresh={onRefresh} onClose={() => { setEditingDefaults(""); defaultsOpener.current?.focus(); }} />
    </section>}
    <div className="project-list">{repositories.length ? repositories.map(alias => {
      const name = status.repository_mappings?.[alias];
      const connected = status.workers?.some(worker => worker.connected && worker.repositories?.includes(alias));
      const latest = (status.jobs || []).filter(job => job.repository === alias).sort((a,b) => (b.created_at || "").localeCompare(a.created_at || ""))[0];
      return <article key={alias} className="project-card"><GitBranch className="size-4" /><div>{name ? <a href={`https://github.com/${name}`} target="_blank" rel="noreferrer">{name}<ArrowUpRight className="size-3" /></a> : <strong>{alias}</strong>}<small>{({ local: "Local", remote: "VM", kubernetes: "Kubernetes" }[host] || "Configured runner")}</small></div><span className={connected ? "project-connected" : "text-muted-foreground"}>{connected ? "Runner connected" : "Waiting for runner"}</span><div className="project-work">{latest ? <a href={`#/runs/${encodeURIComponent(latest.id)}`}>Latest: {jobDisplayTitle(latest)}</a> : <span>No jobs yet</span>}{latest && <State value={latest.state} compact />}<div className="project-row-actions"><Button size="sm" onClick={() => onStart?.(alias)}>Start job</Button>{host && <Button variant="ghost" size="sm" onClick={event => { defaultsOpener.current = event.currentTarget; setEditingDefaults(alias); }}>Edit defaults</Button>}</div></div></article>;
    }) : <p className="dashboard-empty">No projects connected. Complete setup to add your first repository.</p>}</div>
  </section>;
}

function ProjectDefaults({ repository, defaults, host, csrfToken, onRefresh, onClose }) {
 const [recipe, setRecipe] = useState(defaults?.recipe || "");
 const [instructions, setInstructions] = useState(defaults?.instructions || "");
 const [saving, setSaving] = useState(false);
 const [message, setMessage] = useState("");
 async function save(event) {
  event.preventDefault(); setSaving(true); setMessage("");
  try {
   const response = await fetch("/api/v1/projects/defaults", {method:"PUT",headers:{"Content-Type":"application/json","X-Machinist-CSRF":csrfToken},body:JSON.stringify({repository,recipe,instructions})});
   const result = await response.json().catch(() => ({}));
   if (!response.ok) throw new Error(result.error || "Could not save defaults");
   setMessage("Defaults saved. Existing jobs are unchanged."); await onRefresh?.();
  } catch (error) { setMessage(error.message); }
  finally { setSaving(false); }
 }
 return <form className="project-defaults" aria-label={`Defaults for ${repository}`} onSubmit={save}>
  <header><div><h2>Project defaults</h2><p>{repository}</p></div><Button type="button" variant="ghost" size="icon" aria-label="Close project defaults" disabled={saving} onClick={onClose}><X className="size-4" /></Button></header>
  <label><span>Default run mode</span><select className="field-control" value={recipe} disabled={saving} onChange={event => setRecipe(event.target.value)}>
   <option value="">Use existing submission defaults</option><option value="native-v1" disabled={host === "kubernetes"}>Native agents</option><option value="factory">Legacy pipeline</option><option value="single">Single agent</option>
  </select></label>
  <label><span>Default instructions</span><textarea className="field-control" maxLength={2000} value={instructions} readOnly={saving} onChange={event => setInstructions(event.target.value)} /></label>
  <p className="project-hint">Applies to new jobs submitted through the UI or jobs API. Each job can override these defaults. Agent and model use the shared configuration.</p>
  <Button type="submit" size="sm" disabled={saving}>{saving ? "Saving…" : "Save defaults"}</Button>
  {message && <p role="status">{message}</p>}
 </form>;
}
