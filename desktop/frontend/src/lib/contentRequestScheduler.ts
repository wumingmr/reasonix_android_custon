import { RESOURCE_BUDGETS } from "./resourceBudgets";

type Owner = { active: number };

type Task = {
  owner: Owner;
  run: () => void;
  cancel: () => void;
};

class ContentRequestScheduler {
  private active = 0;
  private queue: Task[] = [];

  owner(): Owner { return { active: 0 }; }

  schedule(owner: Owner, run: () => void, cancel: () => void): void {
    this.queue.push({ owner, run, cancel });
    this.drain();
  }

  release(owner: Owner): void {
    owner.active = Math.max(0, owner.active - 1);
    this.active = Math.max(0, this.active - 1);
    this.drain();
  }

  cancel(owner: Owner): void {
    const retained: Task[] = [];
    for (const task of this.queue) {
      if (task.owner === owner) task.cancel();
      else retained.push(task);
    }
    this.queue = retained;
  }

  private drain(): void {
    while (this.active < RESOURCE_BUDGETS.contentReadsGlobal) {
      const index = this.queue.findIndex((task) => task.owner.active < RESOURCE_BUDGETS.contentReadsPerSession);
      if (index < 0) return;
      const [task] = this.queue.splice(index, 1);
      this.active += 1;
      task.owner.active += 1;
      task.run();
    }
  }
}

export const contentRequestScheduler = new ContentRequestScheduler();
