// Only formal generation.created events publish turns. All joins are ID based.
const key = (session, id) => `${session}/${id}`;
const fields = ['eotToSTT', 'sttToLLM', 'llmToTTS', 'ttsToAudio', 'serverTTFA', 'clientTTFA', 'audibleTTFA', 'interruptionLatency', 'total'];
function stamp(value) {
  const match = /^(.*T\d\d:\d\d:\d\d)(?:\.(\d+))?Z$/.exec(value ?? '');
  if (!match) return undefined;
  const seconds = Date.parse(`${match[1]}Z`);
  return Number.isFinite(seconds) ? BigInt(seconds) * 1000000n + BigInt((match[2] ?? '').padEnd(9, '0').slice(0, 9)) : undefined;
}
function elapsed(start, end) {
  return start !== undefined && end !== undefined && end >= start ? Number(end - start) / 1e6 : null;
}
export const formatLatency = ms => ms == null ? '—' : ms < 1000 ? `${Math.round(ms)} ms` : `${(ms / 1000).toFixed(2)} 秒`;
export class TurnHistory {
  turns = [];
  generations = new Map();
  inputs = new Map();
  seen = new Set();
  input(session, id) {
    const k = key(session, id);
    if (!this.inputs.has(k)) this.inputs.set(k, {});
    return this.inputs.get(k);
  }
  generation(session, id) {
    const k = key(session, id);
    if (!this.generations.has(k)) this.generations.set(k, {
      sessionId: session, generationId: id, turnId: null, userText: '', assistantText: '', status: 'pending',
      latency: Object.fromEntries(fields.map(name => [name, null])), times: {}, interruptions: new Map(),
    });
    return this.generations.get(k);
  }
  accept(e, received = performance.now()) {
    const eventKey = key(e.session_id, e.event_id);
    if (this.seen.has(eventKey)) return;
    this.seen.add(eventKey);
    const d = e.data ?? {}, time = stamp(e.timestamp);
    if (e.type === 'input_audio.turn' && d.turn_id && d.state === 'complete') {
      Object.assign(this.input(e.session_id, d.turn_id), {eot: time, received});
    }
    if (e.type === 'input_audio.transcript.final' && d.turn_id) {
      Object.assign(this.input(e.session_id, d.turn_id), {stt: time, userText: d.text});
    }
    const id = e.generation_id || (e.type === 'conversation.item.updated' ? d.generation_id : undefined);
    if (id && !e.type.startsWith('speculation.')) {
      const t = this.generation(e.session_id, id);
      if (e.type === 'conversation.item.updated' && d.role === 'assistant' && d.turn_id) t.turnId = d.turn_id;
      if (e.type === 'generation.created' && !t.number) {
        t.number = this.turns.length + 1; t.times.created = time; this.turns.push(t);
      }
      if (e.type === 'response.text.delta') { t.assistantText += d.text; t.times.llm ??= time; }
      if (e.type === 'response.audio.chunk.started') t.times.audio ??= time;
      if (e.type === 'generation.done') {
        t.times.done = time;
        // Audio generation completion is not playback/conversation completion.
        if (d.source_frames === undefined && t.status === 'pending') t.status = 'completed';
      }
      if (e.type === 'generation.cancelled') { t.times.done ??= time; if (!['failed', 'interrupted'].includes(t.status)) t.status = 'cancelled'; }
      if (e.type === 'error') { t.status = 'failed'; t.times.done ??= time; }
      if (e.type === 'interruption.suspected' && d.interruption_id) t.interruptions.set(d.interruption_id, time);
      if (e.type === 'interruption.confirmed') {
        t.status = 'interrupted'; t.times.done = time;
        t.latency.interruptionLatency = elapsed(t.interruptions.get(d.interruption_id), time);
      }
      if (e.type === 'conversation.item.updated' && d.status === 'interrupted' && t.status !== 'failed') t.status = 'interrupted';
      if (e.type === 'conversation.item.updated' && d.status === 'completed' && t.status === 'pending') t.status = 'completed';
      if (e.type === 'conversation.item.updated' && d.status === 'cancelled' && t.status === 'pending') t.status = 'cancelled';
    }
    this.refresh();
  }
  bindText(session, generation, text) {
    const turn = this.generation(session, generation);
    turn.userText = text; turn.textInput = true; this.refresh();
  }
  audio(session, generation, received = performance.now()) {
    this.generation(session, generation).times.audioReceived ??= received; this.refresh();
  }
  close(session, failed = false) {
    for (const t of this.turns) if (t.sessionId === session && t.status === 'pending') t.status = failed ? 'failed' : 'cancelled';
  }
  refresh() {
    for (const t of this.turns) {
      const input = this.inputs.get(key(t.sessionId, t.turnId));
      if (input?.userText !== undefined) t.userText = input.userText;
      const l = t.latency;
      l.eotToSTT = elapsed(input?.eot, input?.stt);
      l.sttToLLM = elapsed(input?.stt, t.times.llm);
      l.serverTTFA = elapsed(input?.eot, t.times.audio);
      l.total = elapsed(input?.eot ?? (t.textInput ? t.times.created : undefined), t.times.done);
      const client = t.times.audioReceived - input?.received;
      l.clientTTFA = Number.isFinite(client) && client >= 0 ? client : null;
    }
  }
  aggregate() {
    const values = this.turns.filter(t => t.status === 'completed' && t.latency.serverTTFA != null).map(t => t.latency.serverTTFA).sort((a, b) => a - b);
    const percentile = p => values.length ? values[Math.max(0, Math.ceil(values.length * p) - 1)] : null;
    return {samples: values.length, p50: percentile(.5), p95: percentile(.95), min: values[0] ?? null, max: values.at(-1) ?? null};
  }
}
