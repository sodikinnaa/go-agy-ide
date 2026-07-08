const questions = document.querySelectorAll('.question');
const result = document.getElementById('quiz-result');
const answered = new Map();

questions.forEach((question, index) => {
  const buttons = question.querySelectorAll('button');
  const answer = question.dataset.answer;

  buttons.forEach((button) => {
    button.addEventListener('click', () => {
      buttons.forEach((btn) => {
        btn.classList.remove('correct', 'wrong');
        btn.disabled = false;
      });

      const isCorrect = button.dataset.choice === answer;
      button.classList.add(isCorrect ? 'correct' : 'wrong');

      const correctButton = question.querySelector(`[data-choice="${answer}"]`);
      if (correctButton) correctButton.classList.add('correct');

      answered.set(index, isCorrect);
      updateScore();
    });
  });
});

function updateScore() {
  const totalAnswered = answered.size;
  const correct = Array.from(answered.values()).filter(Boolean).length;
  const total = questions.length;

  if (totalAnswered < total) {
    result.textContent = `Skor sementara: ${correct}/${totalAnswered}. Lanjutkan soal berikutnya.`;
    return;
  }

  result.textContent = `Selesai! Skor kamu ${correct}/${total}. ${correct === total ? 'Excellent work!' : 'Coba ulangi lagi besok pagi.'}`;
}
