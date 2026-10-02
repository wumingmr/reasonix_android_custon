export interface SkillWatchDiagnostics {
  physicalWatches: number;
  logicalSubscriptions: number;
  scans: number;
  scannedEntries: number;
  eventsReceived: number;
  notifications: number;
  degradedRoots: number;
  helperRestarts: number;
}

/** Extension runtime doctor report from App.RuntimeDoctor. */
export interface RuntimeDoctorReport {
  text: string;
  publishedGeneration: number;
  allowResume: boolean;
  cleanRollback: boolean;
  hasIrreversible: boolean;
  noOpRebuilds: number;
  fullRebuilds: number;
  subgraphRebuilds: number;
  staleDrops: number;
  admissionRejected: number;
  runtimeOwnerFallbacks: number;
  skillWatch?: SkillWatchDiagnostics;
}
