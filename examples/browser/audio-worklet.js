class PCMPlayerProcessor extends AudioWorkletProcessor {
    constructor() {
        super();

        this.queue = [];
        this.queueOffset = 0;

        this.activeGeneration = null;
        this.paused = false;
        this.pauseID = null;
        this.storedFrames = 0;
        // Float32 mono audio: at 48 kHz this caps retained sample storage at 5.76 MB.
        this.maxStoredFrames = Math.floor(sampleRate * 30);

        this.playedFrames = 0;
        this.playedSourceFrames = 0;
        this.hasSourceFrames = false;
        this.lastReportedFrames = 0;

        // Report playback progress approximately every 100 ms.
        this.reportIntervalFrames =
            Math.max(
                128,
                Math.floor(sampleRate / 10)
            );

        this.port.onmessage = (event) => {
            const message = event.data;

            switch (message.type) {
                case "pause":
                    this.pause(message.generationId, message.interruptionId);
                    break;
                case "resume":
                    this.resume(message.generationId, message.interruptionId);
                    break;
                case "generation":
                    this.setGeneration(
                        message.generationId
                    );
                    break;

                case "audio":
                    this.enqueue(
                        message.generationId,
                        message.samples, message.sourceFrames
                    );
                    break;

                case "clear":
                    this.clear(
                        message.generationId
                    );
                    break;
            }
        };
    }

    setGeneration(generationId) {
        if (
            this.activeGeneration ===
            generationId
        ) {
            return;
        }

        this.queue = [];
        this.queueOffset = 0;
        this.storedFrames = 0;
        this.paused = false;
        this.pauseID = null;

        this.playedFrames = 0;
        this.playedSourceFrames = 0;
        this.hasSourceFrames = false;
        this.lastReportedFrames = 0;

        this.activeGeneration =
            generationId;
    }

    enqueue(generationId, samples, sourceFrames) {
        if (
            !generationId ||
            generationId !==
                this.activeGeneration
        ) {
            return;
        }

        if (!samples) {
            return;
        }

        const data =
            samples instanceof Float32Array
                ? samples
                : new Float32Array(samples);

        if (data.length === 0) {
            return;
        }
        if (this.storedFrames + data.length > this.maxStoredFrames) {
            const interruptionId = this.pauseID;
            this.clear(generationId);
            this.port.postMessage({type: "playback.overflow", generationId, interruptionId});
            return;
        }
        this.storedFrames += data.length;
        if (Number.isSafeInteger(sourceFrames) && sourceFrames >= 0) this.hasSourceFrames = true;

        this.queue.push({
            generationId,
            samples: data,
            sourceFrames: Number.isSafeInteger(sourceFrames) && sourceFrames >= 0 ? sourceFrames : null
        });
    }

    clear(generationId) {
        if (
            generationId &&
            generationId !==
                this.activeGeneration
        ) {
            return;
        }

        // Send the final playback position before discarding
        // the remaining queued audio.
        this.reportProgress(true);

        this.queue = [];
        this.queueOffset = 0;
        this.storedFrames = 0;
        this.paused = false;
        this.pauseID = null;

        this.activeGeneration = null;
    }

    pause(generationId, interruptionId) {
        if (!generationId || generationId !== this.activeGeneration || !interruptionId) return;
        this.paused = true;
        this.pauseID = interruptionId;
        this.reportProgress(true);
        this.port.postMessage({
            type: "playback.paused", generationId, interruptionId,
            playedFrames: this.playedFrames, sampleRate,
            playedSourceFrames: this.hasSourceFrames ? this.sourceProgress() : undefined,
            bufferedFrames: this.storedFrames - this.queueOffset
        });
    }

    resume(generationId, interruptionId) {
        if (!generationId || generationId !== this.activeGeneration || !this.paused || interruptionId !== this.pauseID) return;
        this.paused = false;
        this.pauseID = null;
    }

    sourceProgress() {
        const item = this.queue[0];
        const partial = item && item.sourceFrames !== null
            ? Math.floor(item.sourceFrames * this.queueOffset / item.samples.length) : 0;
        return this.playedSourceFrames + partial;
    }

    reportProgress(force = false) {
        if (!this.activeGeneration) {
            return;
        }

        const delta =
            this.playedFrames -
            this.lastReportedFrames;

        if (
            !force &&
            delta < this.reportIntervalFrames
        ) {
            return;
        }

        this.lastReportedFrames =
            this.playedFrames;

        this.port.postMessage({
            type: "playback.progress",

            generationId:
                this.activeGeneration,

            playedFrames:
                this.playedFrames,

            sampleRate,
            playedSourceFrames: this.hasSourceFrames ? this.sourceProgress() : undefined
        });
    }

    process(inputs, outputs) {
        const output = outputs[0];

        if (
            !output ||
            output.length === 0
        ) {
            return true;
        }

        const frames =
            output[0].length;

        for (
            let channel = 0;
            channel < output.length;
            channel++
        ) {
            output[channel].fill(0);
        }
        if (this.paused) return true;

        let outputOffset = 0;
        let consumedFrames = 0;

        while (
            outputOffset < frames &&
            this.queue.length > 0
        ) {
            const item =
                this.queue[0];

            if (
                item.generationId !==
                this.activeGeneration
            ) {
                this.storedFrames -= item.samples.length;
                this.queue.shift();
                this.queueOffset = 0;
                continue;
            }

            const current =
                item.samples;

            const available =
                current.length -
                this.queueOffset;

            const required =
                frames -
                outputOffset;

            const count =
                Math.min(
                    available,
                    required
                );

            for (
                let i = 0;
                i < count;
                i++
            ) {
                const sample =
                    current[
                        this.queueOffset + i
                    ];

                for (
                    let channel = 0;
                    channel < output.length;
                    channel++
                ) {
                    output[channel][
                        outputOffset + i
                    ] = sample;
                }
            }

            this.queueOffset += count;
            outputOffset += count;
            consumedFrames += count;

            if (
                this.queueOffset >=
                current.length
            ) {
                if (item.sourceFrames !== null) this.playedSourceFrames += item.sourceFrames;
                this.storedFrames -= current.length;
                this.queue.shift();
                this.queueOffset = 0;
            }
        }

        if (consumedFrames > 0) {
            this.playedFrames +=
                consumedFrames;

            this.reportProgress(this.queue.length === 0);
        }

        return true;
    }
}

registerProcessor(
    "pcm-player",
    PCMPlayerProcessor
);
