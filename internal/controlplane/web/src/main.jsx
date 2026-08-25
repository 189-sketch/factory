import React, { useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";

const terminal = new Set(["succeeded", "failed", "timed_out", "cancelled", "skipped"]);

function App() {
  const [status, setStatus] = useState({ jobs: [], workers: [], agents: [], pipelines: [], csrf_token: "" });
  const [selection, setSelection] = useState("");
  const [repository, setRepository] = useState("factory");
  const [prompt, setPrompt] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  async function refresh() {
    const response = await fetch("/api/v1/status", { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error(`Status request failed (${response.status})`);
    const next = await response.json();
    setStatus(next);
    setSelection((current) => current || firstSelection(next));
  }

  useEffect(() => {
    let stopped = false;
    let timer;
    const load = async () => {
      try {
        await refresh();
        if (!stopped) setError("");
      } catch (requestError) {
        if (!stopped) setError(requestError.message);
      }
      if (!stopped) timer = window.setTimeout(load, 2000);
    };
    load();
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, []);

  const choices = useMemo(() => [
    ...status.agents.map((name) => ({ value: `agent:${name}`, label: `Agent · ${name}` })),
    ...status.pipelines.map((name) => ({ value: `pipeline:${name}`, label: `Pipeline · ${name}` })),
  ], [status.agents, status.pipelines]);

  async function submit(event) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    const [kind, name] = selection.split(":", 2);
    try {
      const response = await fetch("/api/v1/jobs", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Factory-CSRF": status.csrf_token },
        body: JSON.stringify({ prompt, repository, [kind]: name }),
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({}));
        throw new Error(body.error || `Submission failed (${response.status})`);
      }
      setPrompt("");
      await refresh();
    } catch (requestError) {
      setError(requestError.message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <main>
      <header>
        <div>
          <p className="eyebrow">Factory control plane</p>
          <h1>Work, from prompt to process.</h1>
        </div>
        <div className="worker-summary">
          <span className={status.workers.length ? "dot online" : "dot"} />
          {status.workers.length} worker{status.workers.length === 1 ? "" : "s"}
        </div>
      </header>

      <section className="panel submit-panel">
        <form onSubmit={submit}>
          <div className="field-row">
            <label>
              Run
              <select value={selection} onChange={(event) => setSelection(event.target.value)} required>
                {choices.map((choice) => <option key={choice.value} value={choice.value}>{choice.label}</option>)}
              </select>
            </label>
            <label>
              Repository key
              <input value={repository} onChange={(event) => setRepository(event.target.value)} required />
            </label>
          </div>
          <label>
            Prompt
            <textarea value={prompt} onChange={(event) => setPrompt(event.target.value)} placeholder="Work on ticket LINEAR-123" required />
          </label>
          <div className="submit-row">
            <p>The prompt is rendered centrally. Workers supply commands, paths, and credentials.</p>
            <button disabled={submitting || !selection}>{submitting ? "Submitting…" : "Submit work"}</button>
          </div>
        </form>
        {error && <p className="error" role="alert">{error}</p>}
      </section>

      <section className="work-section">
        <div className="section-title">
          <h2>Jobs</h2>
          <span>{status.jobs.length}</span>
        </div>
        {status.jobs.length === 0 ? <div className="empty">No work submitted yet.</div> : status.jobs.map((job) => <Job key={job.id} job={job} />)}
      </section>
    </main>
  );
}

function Job({ job }) {
  return (
    <article className="panel job">
      <div className="job-heading">
        <div>
          <p className="job-id">{job.id}</p>
          <h3>{job.prompt}</h3>
          <p>{job.selection_kind} · {job.selection_name} · {job.repository}</p>
        </div>
        <State value={job.state} />
      </div>
      <div className="runs">
        {job.runs.map((run, index) => (
          <div className="run" key={run.id}>
            <span className="step">{String(index + 1).padStart(2, "0")}</span>
            <div>
              <strong>{run.agent}</strong>
              <p>{run.worker_name || run.executor}</p>
              {run.error && <p className="run-error">{run.error}</p>}
            </div>
            <State value={run.state} />
          </div>
        ))}
      </div>
    </article>
  );
}

function State({ value }) {
  return <span className={`state state-${value} ${terminal.has(value) ? "terminal" : ""}`}>{value.replaceAll("_", " ")}</span>;
}

function firstSelection(status) {
  if (status.agents?.length) return `agent:${status.agents[0]}`;
  if (status.pipelines?.length) return `pipeline:${status.pipelines[0]}`;
  return "";
}

createRoot(document.getElementById("root")).render(<App />);
