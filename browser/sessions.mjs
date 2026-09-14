import { randomUUID } from 'node:crypto';
export class BrowserError extends Error {
  constructor(status, message, details) { super(message); this.status = status; this.details = details; }
}
export class Sessions {
  constructor({create, max=4, idleMs=120000, ttlMs=900000, now=Date.now}) {
    Object.assign(this,{create,max,idleMs,ttlMs,now}); this.users = new Map();
  }
  async acquire(user, requested) {
    await this.sweep();
    let session = this.users.get(user);
    if (requested && (!session || session.id !== requested)) throw new BrowserError(404,'Session missing, expired, or owned by another user');
    if (session?.busy) throw new BrowserError(409,'This user already has a browser operation in progress');
    if (!session) {
      if (this.users.size >= this.max) throw new BrowserError(429,'Browser capacity reached; retry after an idle session expires');
      // Reservation occurs before launching; concurrent users cannot exceed capacity.
      session = {id:randomUUID(),user,created:this.now(),last:this.now(),busy:true,refs:new Map(),generation:0};
      this.users.set(user,session);
      try { Object.assign(session,await this.create()); } catch (e) { this.users.delete(user); throw e; }
    } else session.busy = true;
    return session;
  }
  release(session) { if (this.users.get(session.user) === session) { session.last=this.now();session.busy=false; } }
  async close(session) {
    if (this.users.get(session.user) !== session) return;
    this.users.delete(session.user);
    await session.browser?.close().catch(()=>{});
  }
  async sweep() {
    const now=this.now();
    await Promise.all([...this.users.values()].filter(s=>!s.busy&&(now-s.last>=this.idleMs||now-s.created>=this.ttlMs)).map(s=>this.close(s)));
  }
  async shutdown() { await Promise.all([...this.users.values()].map(s=>this.close(s))); }
}

// Tuple encoding avoids collisions between caller-controlled user/assistant IDs.
export const sessionOwner = input => JSON.stringify([input.user_id,input.agent_id||"default"]);
