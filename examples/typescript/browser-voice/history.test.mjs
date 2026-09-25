import test from 'node:test';
import assert from 'node:assert/strict';
import {TurnHistory, formatLatency} from './history.mjs';
let sequence = 0;
const event = (type, ms, data = {}, generation_id, session_id = 's') => ({type, data, generation_id, session_id, event_id: `e${++sequence}`, timestamp: new Date(Date.UTC(2026, 0, 1) + ms).toISOString()});
function voice(h, id, offset = 0, ttfa = 900) {
  h.accept(event('input_audio.turn', offset, {turn_id: id, state: 'complete'}), offset);
  h.accept(event('input_audio.transcript.final', offset + 400, {turn_id: id, text: `user ${id}`}), offset + 420);
  h.accept(event('generation.created', offset + 410, {}, id));
  h.accept(event('response.text.delta', offset + 700, {text: `answer ${id}`}, id));
  h.accept(event('response.audio.chunk.started', offset + ttfa, {}, id));
  h.audio('s', id, offset + 950);
  // Metadata can arrive after text/audio; never associate with the latest turn.
  h.accept(event('conversation.item.updated', offset + 420, {role: 'assistant', turn_id: id, status: 'pending'}, id));
}
test('joins delayed metadata by IDs and retains exact event precision', () => {
  const h = new TurnHistory(); voice(h, 'a'); voice(h, 'b', 2000);
  h.accept(event('response.text.delta', 3000, {text: '!'}, 'a'));
  const [a, b] = h.turns;
  assert.equal(a.userText, 'user a'); assert.equal(a.assistantText, 'answer a!'); assert.equal(b.assistantText, 'answer b');
  assert.equal(a.latency.eotToSTT, 400); assert.equal(a.latency.sttToLLM, 300); assert.equal(a.latency.serverTTFA, 900); assert.equal(a.latency.clientTTFA, 950);
  assert.equal(a.latency.llmToTTS, null); assert.equal(a.latency.ttsToAudio, null); assert.equal(a.latency.audibleTTFA, null);
  const precise = event('input_audio.transcript.final', 400, {turn_id: 'a', text: 'precise'});
  precise.timestamp = '2026-01-01T00:00:00.400123456Z'; h.accept(precise);
  assert.equal(a.latency.eotToSTT, 400.123456);
});
test('audio generation.done is not conversation completion; terminal states stay excluded', () => {
  const h = new TurnHistory(); voice(h, 'a');
  h.accept(event('generation.done', 1000, {source_frames: 16000}, 'a'));
  assert.equal(h.aggregate().samples, 0);
  h.accept(event('conversation.item.updated', 2000, {role: 'assistant', turn_id: 'a', status: 'completed'}, 'a'));
  assert.deepEqual(h.aggregate(), {samples: 1, p50: 900, p95: 900, min: 900, max: 900});
  h.accept(event('interruption.suspected', 2100, {interruption_id: 'i'}, 'a'));
  h.accept(event('interruption.confirmed', 2200, {interruption_id: 'i'}, 'a'));
  h.accept(event('generation.cancelled', 2300, {}, 'a'));
  assert.equal(h.turns[0].status, 'interrupted'); assert.equal(h.turns[0].latency.interruptionLatency, 100); assert.equal(h.aggregate().samples, 0);
  voice(h, 'b'); h.accept(event('error', 1000, {}, 'b')); h.accept(event('generation.cancelled', 1100, {}, 'b'));
  assert.equal(h.turns[1].status, 'failed'); assert.equal(h.aggregate().samples, 0);
});
test('speculation is invisible until formal creation; duplicates and session IDs are isolated', () => {
  const h = new TurnHistory();
  h.accept(event('speculation.ready', 0, {turn_id: 't', text: 'private'}, 'a'));
  h.accept(event('speculation.promoted', 1, {turn_id: 't'}, 'a'));
  assert.equal(h.turns.length, 0);
  const created = event('generation.created', 2, {}, 'a'); h.accept(created); h.accept(created);
  h.accept(event('generation.created', 3, {}, 'a', 'other'));
  h.bindText('other', 'a', 'different session');
  assert.equal(h.turns.length, 2); assert.equal(h.turns[0].userText, ''); assert.equal(h.turns[1].userText, 'different session');
  h.close('s'); assert.equal(h.turns[0].status, 'cancelled'); assert.equal(h.turns[1].status, 'pending');
});
test('missing correlation and unmatched interruption do not fabricate measurements', () => {
  const h = new TurnHistory();
  h.accept(event('generation.created', 100, {}, 'text')); h.bindText('s', 'text', 'hello');
  h.accept(event('generation.done', 1100, {}, 'text'));
  assert.equal(h.turns[0].latency.total, 1000); assert.equal(h.turns[0].latency.serverTTFA, null); assert.equal(h.aggregate().samples, 0);
  h.accept(event('interruption.confirmed', 1200, {interruption_id: 'missing'}, 'text'));
  assert.equal(h.turns[0].latency.interruptionLatency, null);
  assert.equal(formatLatency(999.12), '999 ms'); assert.equal(formatLatency(1240.123), '1.24 秒'); assert.equal(formatLatency(null), '—');
});
test('nearest rank aggregates include only completed measured turns', () => {
  const h = new TurnHistory();
  for (let i = 1; i <= 20; i++) {
    voice(h, String(i), 0, i * 100);
    h.accept(event('response.audio.chunk.started', 99999, {}, String(i)));
    h.accept(event('generation.done', 3000, {source_frames: 100}, String(i)));
    h.accept(event('conversation.item.updated', 4000, {role: 'assistant', turn_id: String(i), status: 'completed'}, String(i)));
  }
  assert.deepEqual(h.aggregate(), {samples: 20, p50: 1000, p95: 1900, min: 100, max: 2000});
  h.accept(event('generation.cancelled', 5000, {}, '20')); assert.equal(h.aggregate().samples, 19);
});
