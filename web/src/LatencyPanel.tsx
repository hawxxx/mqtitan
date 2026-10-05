import type {Snapshot} from './model';
import {latency,number} from './model';

export function LatencyPanel({snapshot:s}:{snapshot:Snapshot}){
 const bins=s.histogram||[];
 const count=s.publishLatencyCount||bins.reduce((sum,b)=>sum+b.count,0);
 const peak=Math.max(1,...bins.map(b=>b.count));
 const values:[string,number|undefined][]=[['P50',s.p50],['P75',s.p75],['P90',s.p90],['P95',s.p95],['P99',s.p99],['P99.9',s.p999]];
 return <section className="panel latency-distribution">
  <div className="panel-heading"><div><h2>Latency distribution</h2><p>{number(count)} measured publish samples</p></div><span className="subdued">Cumulative run distribution</span></div>
  <div className="percentile-strip">{values.map(([label,value])=><div key={label}><span>{label}</span><strong className="mono">{count&&value!==undefined?latency(value):'—'}</strong></div>)}</div>
  {count>0&&bins.length>0?<div className="latency-histogram" role="img" aria-label="Publish latency histogram">{bins.map(b=><div className="histogram-bin" key={b.upperBoundNs} title={`≤ ${latency(b.upperBoundNs)}: ${b.count} samples`}><div className="histogram-bar-space"><div className="histogram-bar" style={{height:`${b.count/peak*100}%`}}/></div><span>{b.upperBoundNs<1e6?`${b.upperBoundNs/1000}µs`:b.upperBoundNs<1e9?`${b.upperBoundNs/1e6}ms`:`${b.upperBoundNs/1e9}s`}</span></div>)}</div>:<p className="histogram-empty">No publish latency samples yet</p>}
  <div className="panel-footer"><span>Upper bounds · logarithmic bucket spacing</span><span>End-to-end P95: {s.correlationSamples?latency(s.endToEndP95||0):'No correlated samples'}</span></div>
 </section>;
}
