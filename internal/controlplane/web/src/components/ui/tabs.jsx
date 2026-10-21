import React, { useId, useState, useEffect, useRef } from "react";
import { cn } from "@/lib/utils";

export function Tabs({ items, label, value, onValueChange }) {
  const [selected, setSelected] = useState(items[0].id);
  const id = useId();
  const current=value ?? selected;
 const active = items.some(item => item.id === current) ? current : items[0].id;
 const previousValue=useRef(value);
 useEffect(()=>{if(value!==undefined && previousValue.current!==value) document.getElementById(`${id}-tab-${active}`)?.focus();previousValue.current=value;},[value,id,active]);
 const choose=next=>{setSelected(next);onValueChange?.(next);};
  function navigate(event, index) {
    let next;
    if (event.key === "ArrowRight") next = (index + 1) % items.length;
    if (event.key === "ArrowLeft") next = (index + items.length - 1) % items.length;
    if (event.key === "Home") next = 0;
    if (event.key === "End") next = items.length - 1;
    if (next === undefined) return;
    event.preventDefault();
    choose(items[next].id);
    document.getElementById(`${id}-tab-${items[next].id}`)?.focus();
  }
  return <div>
    <div role="tablist" aria-label={label} className="mb-6 flex gap-4 overflow-x-auto border-b border-border">
      {items.map((item, index) => <button key={item.id} id={`${id}-tab-${item.id}`} type="button" role="tab" aria-selected={active === item.id} aria-controls={`${id}-panel-${item.id}`} tabIndex={active === item.id ? 0 : -1} onKeyDown={event => navigate(event, index)} onClick={() => choose(item.id)} className={cn("-mb-px border-b-2 px-0 py-3 text-sm font-medium transition-colors focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2", active === item.id ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground")}>{item.label}</button>)}
    </div>
    {items.map(item => <section key={item.id} id={`${id}-panel-${item.id}`} role="tabpanel" aria-labelledby={`${id}-tab-${item.id}`} hidden={active !== item.id} tabIndex={0} className="focus-visible:outline-2 focus-visible:outline-ring">{item.content}</section>)}
  </div>;
}
